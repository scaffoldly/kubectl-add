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
	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add/tunnel/apiserver"
	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add/tunnel/deployment"
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
	install    bool
	remove     bool
	onURL      func(ctx context.Context, publicURL string) (cleanup func(), err error)

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

// WithInstall runs a persistent in-cluster tunnel (a libtunnel Deployment)
// instead of a local one, and prints its public URL.
func (t *Tunnel) WithInstall(install bool) *Tunnel {
	t.install = install
	return t
}

// WithRemove deletes a previously installed in-cluster tunnel.
func (t *Tunnel) WithRemove(remove bool) *Tunnel {
	t.remove = remove
	return t
}

// WithOnPublicURL registers a callback invoked once the public URL is known
// (both the local and installed tunnels), e.g. to write a kubeconfig context.
// It may return a cleanup func, which the local tunnel runs on teardown (its
// public URL dies with the process); the installed tunnel ignores it, since it
// outlives the command. A callback error is logged and does not fail the tunnel.
func (t *Tunnel) WithOnPublicURL(fn func(ctx context.Context, publicURL string) (cleanup func(), err error)) *Tunnel {
	t.onURL = fn
	return t
}

// announce reports the ready tunnel: it runs the onURL hook (e.g. writing a
// kubeconfig context, which prints its own confirmation) or, when no hook is
// set, prints where the public URL forwards to. It returns the hook's cleanup
// (nil if none), for the caller to run on teardown.
func (t *Tunnel) announce(ctx context.Context, describe, targetURL, publicURL string) (cleanup func()) {
	t.log.Info("tunnel ready", "target", describe, "url", publicURL)
	if t.onURL != nil {
		clean, err := t.onURL(ctx, publicURL)
		if err != nil {
			t.log.Warn("could not update kubeconfig context", "err", err)
		}
		return clean
	}
	fmt.Fprintf(os.Stderr, "%s -> %s\n", publicURL, targetURL)
	return nil
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
	if t.install && t.remove {
		return fmt.Errorf("--install and --remove are mutually exclusive")
	}
	t.log = t.logger()

	if t.remove {
		return t.runRemove(ctx)
	}
	if t.install {
		return t.runInstall(ctx)
	}

	if name, port, isService := parseTarget(t.target); isService {
		return t.runService(ctx, name, port)
	}
	return t.runAPIServer(ctx)
}

// runInstall deploys the persistent in-cluster tunnel and announces its URL.
func (t *Tunnel) runInstall(ctx context.Context) error {
	name, port, isService := parseTarget(t.target)
	url, describe, origin, err := deployment.Install(ctx, t.restConfig, t.namespace, name, port, isService, t.log)
	if err != nil {
		return err
	}
	t.announce(ctx, describe, origin, url)
	return nil
}

// runRemove deletes the persistent in-cluster tunnel.
func (t *Tunnel) runRemove(ctx context.Context) error {
	name, _, isService := parseTarget(t.target)
	return deployment.Remove(ctx, t.restConfig, t.namespace, name, isService, t.log)
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
		WithInterceptor(apiserver.InterceptWatch(t.log)).
		WithLocalURL(origin)

	return t.serve(ctx, tun, "apiserver "+origin.Host, origin.String(), nil)
}

// runService tunnels to a Service through the API server's services/proxy
// subresource. The subresource needs a path prefix that WithLocalURL cannot
// carry, so this path runs a local reverse proxy — which also lets it verify
// the API server against the cluster CA — and hands its loopback listener to
// libtunnel. The proxy injects no credentials, so the caller needs the RBAC to
// reach services/proxy.
func (t *Tunnel) runService(ctx context.Context, name, port string) error {
	target, err := url.Parse(t.restConfig.Host)
	if err != nil {
		return fmt.Errorf("parsing API server URL %q: %w", t.restConfig.Host, err)
	}
	transport, err := t.transport()
	if err != nil {
		return err
	}

	svc := name
	if port != "" {
		svc = name + ":" + port
	}
	pathPrefix := fmt.Sprintf("/api/v1/namespaces/%s/services/%s/proxy", t.namespace, svc)
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

	targetURL := fmt.Sprintf("%s://%s%s", target.Scheme, target.Host, pathPrefix)
	return t.serve(ctx, tun, fmt.Sprintf("service %s/%s", t.namespace, name), targetURL, func() { _ = server.Close() })
}

// serve waits for the tunnel to come up, prints its public URL, and blocks
// until the context is canceled or the tunnel fails. onShutdown, if set, is
// called on the way out (e.g. to close a listener-backed origin).
func (t *Tunnel) serve(ctx context.Context, tun libtunnel.TunnelV1, describe, targetURL string, onShutdown func()) error {
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

	cleanup := t.announce(ctx, describe, targetURL, publicURL.String())

	shutdown := func() {
		if onShutdown != nil {
			onShutdown()
		}
		// The local tunnel's public URL dies with the process, so retire the
		// kubeconfig context it added.
		if cleanup != nil {
			cleanup()
		}
	}
	select {
	case <-ctx.Done():
		shutdown()
		return nil
	case <-tun.Done():
		shutdown()
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
// is a Service, optionally with a ":port" suffix. Returns the bare service
// name, the port ("" if none), and whether the target is a Service.
func parseTarget(target string) (name, port string, isService bool) {
	target = strings.TrimSpace(target)
	for _, prefix := range []string{"svc/", "service/", "services/"} {
		if rest, ok := strings.CutPrefix(target, prefix); ok {
			target = rest
			break
		}
	}
	if target == "" || target == "kubernetes" {
		return "", "", false
	}
	if i := strings.LastIndex(target, ":"); i >= 0 {
		return target[:i], target[i+1:], true
	}
	return target, "", true
}

// interceptWatch is a libtunnel interceptor that logs each streaming
// (?watch=true) request passing through the tunnel, then lets it proxy to the
// origin unchanged (the returned ctx is the default origin handler).
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
