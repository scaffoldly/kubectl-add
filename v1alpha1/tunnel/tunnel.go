// Package tunnel exposes the Kubernetes API server — or a Service reachable
// through it — to the public internet through a Cloudflare quick tunnel
// (github.com/cnuss/libtunnel), driven entirely in-process.
//
// The tunnel is a raw forwarder that injects no credentials: callers hitting
// the public URL authenticate themselves exactly as they would against the API
// server directly. The default (API server) target points libtunnel straight
// at the current context's cluster URL (WithLocalURL), which skips origin TLS
// verification. A Service target routes through the API server's services/proxy
// subresource — a path WithLocalURL cannot carry — so it runs a local reverse
// proxy that re-originates to the API server verified against the cluster CA;
// the caller needs the matching services/proxy RBAC.
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
	debug      bool
	verbose    bool

	log *slog.Logger
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

// WithDebug emits the underlying tunnel's own logs (and the tunnel's progress).
func (t *Tunnel) WithDebug(debug bool) *Tunnel {
	t.debug = debug
	return t
}

// WithVerbose emits the tunnel's progress logs.
func (t *Tunnel) WithVerbose(verbose bool) *Tunnel {
	t.verbose = verbose
	return t
}

// logger builds the tunnel-scoped logger. Quiet by default (WARN — only the
// public URL, printed to stdout, is visible); --verbose surfaces progress at
// INFO, --debug the underlying tunnel's logs at DEBUG. Scoped to this tunnel
// rather than mutating slog.Default, so a library caller's logging is untouched.
func (t *Tunnel) logger() *slog.Logger {
	level := slog.LevelWarn
	if t.verbose {
		level = slog.LevelInfo
	}
	if t.debug {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
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
	t.log = t.logger()

	if name, isService := parseTarget(t.target); isService {
		return t.runService(ctx, name)
	}
	return t.runAPIServer(ctx)
}

// runAPIServer tunnels straight to the current context's cluster URL. libtunnel
// dials the API server itself as the origin (WithLocalURL), skipping origin TLS
// verification — the API server's private-CA cert would otherwise fail — and
// forwarding requests untouched, so callers still authenticate themselves.
//
// A URL origin has no tunnel-owned listener, so canceling the context is the
// teardown handle: WithContext propagates cancellation into libtunnel's engine.
func (t *Tunnel) runAPIServer(ctx context.Context) error {
	host, err := url.Parse(t.restConfig.Host)
	if err != nil {
		return fmt.Errorf("parsing API server URL %q: %w", t.restConfig.Host, err)
	}
	// WithLocalURL uses only the scheme and host; drop everything else.
	origin := &url.URL{Scheme: host.Scheme, Host: host.Host}

	tun := libtunnel.New(libtunnel.Cloudflare()).
		WithLogger(t.log).
		WithContext(ctx).
		WithLocalURL(origin)

	return t.serve(ctx, tun, "apiserver "+origin.Host, nil)
}

// runService tunnels to a Service through the API server's services/proxy
// subresource. The subresource needs a path prefix that WithLocalURL cannot
// carry, so this path runs a local reverse proxy — which also lets it verify
// the API server against the cluster CA — and hands its loopback listener to
// libtunnel. The proxy injects no credentials, so the caller needs the RBAC to
// reach services/proxy.
func (t *Tunnel) runService(ctx context.Context, name string) error {
	target, err := url.Parse(t.restConfig.Host)
	if err != nil {
		return fmt.Errorf("parsing API server URL %q: %w", t.restConfig.Host, err)
	}
	transport, err := t.transport()
	if err != nil {
		return err
	}

	pathPrefix := fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy", t.namespace, name)
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		// Stream responses as they arrive so watch/exec/log endpoints work.
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			req.URL.Path = singleJoiningSlash(pathPrefix, req.URL.Path)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			t.log.Debug("tunnel upstream error", "err", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}

	tun := libtunnel.New(libtunnel.Cloudflare()).
		WithLogger(t.log).
		WithContext(ctx)

	lis := tun.Listener()
	if lis == nil {
		return fmt.Errorf("opening tunnel: %w", tun.Err())
	}
	// libtunnel dials this loopback listener as its origin; the reverse proxy
	// re-originates (CA-verified) to the API server's services/proxy path.
	server := &http.Server{Handler: proxy}
	go func() { _ = server.Serve(lis) }()

	return t.serve(ctx, tun, fmt.Sprintf("service %s/%s", t.namespace, name), func() { _ = server.Close() })
}

// serve waits for the tunnel to come up, prints its public URL, and blocks
// until the context is canceled or the tunnel fails. onShutdown, if set, is
// called on the way out (e.g. to close a listener-backed origin).
func (t *Tunnel) serve(ctx context.Context, tun libtunnel.TunnelV1, describe string, onShutdown func()) error {
	t.log.Info("opening tunnel", "target", describe)
	publicURL := tun.URL()
	if publicURL == nil {
		if onShutdown != nil {
			onShutdown()
		}
		if err := tun.Err(); err != nil {
			return fmt.Errorf("opening tunnel: %w", err)
		}
		return ctx.Err()
	}

	t.log.Info("tunnel ready", "target", describe, "url", publicURL)
	fmt.Fprintln(os.Stdout, publicURL)

	select {
	case <-ctx.Done():
		if onShutdown != nil {
			onShutdown()
		}
		return nil
	case <-tun.Done():
		if onShutdown != nil {
			onShutdown()
		}
		return tun.Err()
	}
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
