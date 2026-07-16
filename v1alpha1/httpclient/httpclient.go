// Package httpclient provides the one shared http.Client the tool uses for
// all outbound requests. A single client means a single connection pool
// (connections to the same host are reused across the resolver and the
// fetcher) and one place to configure hygiene: a User-Agent identifying the
// build, a request timeout, and redirect logging. Callers that need
// per-request behavior vary the request (context, headers), not the client.
package httpclient

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/version"
)

// timeout bounds a single request (including body read) across all call sites.
const timeout = 30 * time.Second

// maxRedirects caps redirect chains (e.g. k8s.io short links to raw content).
const maxRedirects = 10

var shared = &http.Client{
	Timeout:   timeout,
	Transport: &userAgentTransport{base: http.DefaultTransport},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		slog.Debug("following redirect", "from", via[len(via)-1].URL, "to", req.URL, "hop", len(via))
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	},
}

// Default returns the shared http.Client. Do not mutate it; construct
// per-request state on the request instead.
func Default() *http.Client { return shared }

// VersionHeader carries the build version as a dedicated header, so proxies
// and routers can match on it directly instead of parsing the User-Agent.
const VersionHeader = "X-Kubectl-Add-Version"

// userAgentTransport stamps build-identifying headers on every request that
// doesn't already carry them, then delegates to the base transport.
type userAgentTransport struct{ base http.RoundTripper }

func (t *userAgentTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", version.UserAgent())
	}
	if req.Header.Get(VersionHeader) == "" {
		req.Header.Set(VersionHeader, version.String())
	}
	return t.base.RoundTrip(req)
}
