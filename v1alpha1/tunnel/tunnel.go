// Package tunnel exposes the Kubernetes API server — or a Service reachable
// through it — to the public internet through a Cloudflare quick tunnel
// (github.com/cnuss/libtunnel), driven entirely in-process.
//
// The tunnel is a raw forwarder: it re-originates TLS to the API server
// trusting only the cluster CA and injects no credentials of its own. Callers
// hitting the public URL must authenticate themselves exactly as they would
// against the API server directly (a Service target routes through the API
// server's services/proxy subresource, so the caller needs the matching RBAC).
package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/cnuss/libtunnel"
	"k8s.io/client-go/rest"
)

// Tunnel is a fluent builder for a single public tunnel. Configure it with the
// With* methods and call Run to open the tunnel and block until the context is
// canceled.
type Tunnel struct {
	restConfig *rest.Config
	namespace  string
	target     string
	ctx        context.Context

	err error
}

// New returns a new Tunnel builder.
func New() *Tunnel { return &Tunnel{} }

// WithRESTConfig sets the cluster connection whose API server is tunneled and
// whose CA is trusted when re-originating TLS.
func (t *Tunnel) WithRESTConfig(config *rest.Config) *Tunnel {
	t.restConfig = config
	return t
}

// WithNamespace scopes a Service target. Ignored for the API server target.
func (t *Tunnel) WithNamespace(namespace string) *Tunnel {
	t.namespace = namespace
	return t
}

// WithTarget sets what to tunnel to: empty or "kubernetes" (optionally
// "svc/"-prefixed) is the API server; any other "[svc/]name" is that Service
// in the configured namespace, reached through the API server's proxy
// subresource.
func (t *Tunnel) WithTarget(target string) *Tunnel {
	t.target = target
	return t
}

// WithContext threads cancellation into Run; closing it (e.g. on SIGINT) tears
// the tunnel down. Defaults to context.Background.
func (t *Tunnel) WithContext(ctx context.Context) *Tunnel {
	t.ctx = ctx
	return t
}

// Run opens the tunnel, prints its public URL to stdout, and blocks until the
// context is canceled or the tunnel fails.
func (t *Tunnel) Run(ctx context.Context) error {
	if t.err != nil {
		return t.err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if t.restConfig == nil {
		return fmt.Errorf("no REST config: WithRESTConfig is required")
	}

	proxy, describe, err := t.reverseProxy()
	if err != nil {
		return err
	}

	tun := libtunnel.New(libtunnel.Cloudflare()).
		WithLogger(slog.Default()).
		WithContext(ctx)

	lis := tun.Listener()
	if lis == nil {
		return fmt.Errorf("opening tunnel: %w", tun.Err())
	}

	// Serve the raw forwarder on the loopback listener libtunnel dials as its
	// origin; the edge terminates TLS at Cloudflare and forwards plain HTTP to
	// this listener, which re-originates to the API server.
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(lis) }()

	slog.Info("opening tunnel", "target", describe)
	publicURL := tun.URL()
	if publicURL == nil {
		if err := tun.Err(); err != nil {
			return fmt.Errorf("opening tunnel: %w", err)
		}
		return ctx.Err()
	}

	slog.Info("tunnel ready", "target", describe, "url", publicURL)
	fmt.Fprintln(os.Stdout, publicURL)

	select {
	case <-ctx.Done():
		// Deliberate shutdown: closing the listener retires the edge.
		_ = server.Close()
		return nil
	case <-tun.Done():
		_ = server.Close()
		return tun.Err()
	}
}

// reverseProxy builds the raw forwarder to the resolved target and a
// human-readable description of it. The transport trusts only the cluster CA
// and carries no client credentials — callers authenticate themselves.
func (t *Tunnel) reverseProxy() (*httputil.ReverseProxy, string, error) {
	target, err := url.Parse(t.restConfig.Host)
	if err != nil {
		return nil, "", fmt.Errorf("parsing API server URL %q: %w", t.restConfig.Host, err)
	}

	transport, err := t.transport()
	if err != nil {
		return nil, "", err
	}

	name, isService := parseTarget(t.target)
	var pathPrefix, describe string
	if isService {
		pathPrefix = fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy", t.namespace, name)
		describe = fmt.Sprintf("service %s/%s", t.namespace, name)
	} else {
		describe = fmt.Sprintf("apiserver %s", target.Host)
	}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		// Stream responses as they arrive so watch/exec/log endpoints work.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			if pathPrefix != "" {
				req.URL.Path = singleJoiningSlash(pathPrefix, req.URL.Path)
			}
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			slog.Debug("tunnel upstream error", "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	return proxy, describe, nil
}

// transport dials the API server over TLS trusting the cluster CA, with no
// client certificate or bearer token — the raw-forward contract.
func (t *Tunnel) transport() (*http.Transport, error) {
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS12}
	tc := t.restConfig.TLSClientConfig
	tlsConf.InsecureSkipVerify = tc.Insecure
	if tc.ServerName != "" {
		tlsConf.ServerName = tc.ServerName
	}

	caData := tc.CAData
	if len(caData) == 0 && tc.CAFile != "" {
		data, err := os.ReadFile(tc.CAFile)
		if err != nil {
			return nil, fmt.Errorf("reading cluster CA %q: %w", tc.CAFile, err)
		}
		caData = data
	}
	if len(caData) > 0 && !tc.Insecure {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return nil, fmt.Errorf("parsing cluster CA certificate")
		}
		tlsConf.RootCAs = pool
	}

	return &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		TLSClientConfig:   tlsConf,
		ForceAttemptHTTP2: true,
	}, nil
}

// parseTarget resolves the positional argument into a target. Empty or
// "kubernetes" (with or without a svc/ prefix) is the API server; anything else
// is a Service of that name. Returns the bare service name and whether the
// target is a Service.
func parseTarget(target string) (name string, isService bool) {
	target = strings.TrimSpace(target)
	for _, prefix := range []string{"svc/", "service/", "services/"} {
		if rest, ok := strings.CutPrefix(target, prefix); ok {
			target = rest
			break
		}
	}
	if target == "" || target == "kubernetes" {
		return "", false
	}
	return target, true
}

// singleJoiningSlash joins two URL path segments with exactly one slash.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
