package remote

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"k8s.io/client-go/rest"
)

// credential is the caller's own cluster credential, forwarded to the runner
// pod so the apply runs as the connected user — never an escalated identity.
// Exactly one of token or (certPEM, keyPEM) is set.
type credential struct {
	token   string
	certPEM []byte
	keyPEM  []byte
}

// callerCredential resolves the connected user's credential from the REST
// config, so the remote kubectl authenticates as them:
//
//   - a client certificate (kubeadm, minikube, docker-desktop, …),
//   - a static bearer token (ServiceAccount kubeconfig, some providers), or
//   - a dynamic token from an exec/auth-provider plugin (EKS, GKE, OIDC),
//     captured from the Authorization header it produces.
//
// It fails closed rather than falling back to any broader identity.
func callerCredential(cfg *rest.Config) (*credential, error) {
	if cert, key, err := clientCert(cfg); err != nil {
		return nil, err
	} else if len(cert) > 0 && len(key) > 0 {
		slog.Debug("forwarding caller client certificate")
		return &credential{certPEM: cert, keyPEM: key}, nil
	}

	if token, err := staticToken(cfg); err != nil {
		return nil, err
	} else if token != "" {
		slog.Debug("forwarding caller bearer token")
		return &credential{token: token}, nil
	}

	if cfg.ExecProvider != nil || cfg.AuthProvider != nil {
		token, err := tokenFromAuthPlugin(cfg)
		if err != nil {
			return nil, err
		}
		if token != "" {
			slog.Debug("forwarding caller token from auth plugin")
			return &credential{token: token}, nil
		}
	}

	return nil, fmt.Errorf("remote: cannot determine the connected user's credential " +
		"(no client certificate, bearer token, or supported auth plugin); " +
		"apply would not run as you")
}

// clientCert returns the caller's client certificate and key, from inline
// data or referenced files.
func clientCert(cfg *rest.Config) (cert, key []byte, err error) {
	cert = cfg.CertData
	if len(cert) == 0 && cfg.CertFile != "" {
		if cert, err = os.ReadFile(cfg.CertFile); err != nil {
			return nil, nil, fmt.Errorf("remote: reading client certificate: %w", err)
		}
	}
	key = cfg.KeyData
	if len(key) == 0 && cfg.KeyFile != "" {
		if key, err = os.ReadFile(cfg.KeyFile); err != nil {
			return nil, nil, fmt.Errorf("remote: reading client key: %w", err)
		}
	}
	return cert, key, nil
}

// staticToken returns the caller's bearer token from the config or its file.
func staticToken(cfg *rest.Config) (string, error) {
	if cfg.BearerToken != "" {
		return cfg.BearerToken, nil
	}
	if cfg.BearerTokenFile != "" {
		token, err := os.ReadFile(cfg.BearerTokenFile)
		if err != nil {
			return "", fmt.Errorf("remote: reading bearer token file: %w", err)
		}
		return strings.TrimSpace(string(token)), nil
	}
	return "", nil
}

// tokenFromAuthPlugin resolves the bearer token an exec or auth-provider
// plugin injects, by building the config's transport and capturing the
// Authorization header it sets on an outgoing request. The request is not
// sent to the network — a stub round-tripper records the header the auth
// layer produced and returns immediately.
func tokenFromAuthPlugin(cfg *rest.Config) (string, error) {
	capture := &captureRoundTripper{}
	c := rest.CopyConfig(cfg)
	// Replace the base transport with the capture stub; TLS settings must be
	// cleared for a custom transport to be accepted.
	c.Transport = capture
	c.TLSClientConfig = rest.TLSClientConfig{}

	rt, err := rest.TransportFor(c)
	if err != nil {
		return "", fmt.Errorf("remote: building auth transport: %w", err)
	}

	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/version", nil)
	if err != nil {
		return "", fmt.Errorf("remote: building auth probe: %w", err)
	}
	// The auth wrapper injects Authorization before delegating to capture.
	if _, err := rt.RoundTrip(req); err != nil {
		return "", fmt.Errorf("remote: resolving auth plugin credential: %w", err)
	}

	const prefix = "Bearer "
	if !strings.HasPrefix(capture.auth, prefix) {
		return "", nil
	}
	return strings.TrimPrefix(capture.auth, prefix), nil
}

// captureRoundTripper records the Authorization header of the request it
// receives and returns a stub 200 response without touching the network.
type captureRoundTripper struct {
	auth string
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	c.auth = req.Header.Get("Authorization")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Header:     make(http.Header),
	}, nil
}
