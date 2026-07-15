package http

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/kustomize"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/yaml"
)

// Resolver is the http transport: it recognizes resources reachable over
// http(s) and sniffs their content to determine the artifact format. It is
// the fallback transport: anything it can't identify is treated as a yaml
// manifest.
type Resolver struct {
	client *http.Client
}

func New() *Resolver {
	return &Resolver{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *Resolver) Name() string {
	return "http"
}

// Detect reports whether the resource is an http(s) URL.
func (r *Resolver) Detect(resource string) bool {
	return strings.HasPrefix(resource, "http://") || strings.HasPrefix(resource, "https://")
}

// Resolve sniffs the URL to determine the artifact format: packaged helm
// charts (.tgz) route to helm, everything else falls back to yaml.
func (r *Resolver) Resolve(resource string) (*resolve.Resolution, error) {
	u, err := url.Parse(resource)
	if err != nil {
		return nil, fmt.Errorf("http resolver: parsing %q: %w", resource, err)
	}

	if strings.HasSuffix(u.Path, ".tgz") {
		slog.Debug("sniffed packaged helm chart", "url", u)
		return helm.Resolution(r.Name(), u), nil
	}

	switch path.Base(u.Path) {
	case "kustomization.yaml", "kustomization.yml", "Kustomization":
		slog.Debug("sniffed kustomization", "url", u)
		return kustomize.Resolution(r.Name(), u), nil
	case "Chart.yaml", "Chart.yml":
		slog.Debug("sniffed helm chart", "url", u)
		return helm.Resolution(r.Name(), u), nil
	}

	// An extension-less URL may be a helm chart repository; probe its
	// index.yaml before assuming a plain manifest.
	if path.Ext(strings.TrimSuffix(u.Path, "/")) == "" && r.isChartRepo(u) {
		slog.Debug("sniffed helm chart repo", "url", u)
		return helm.Resolution(r.Name(), u), nil
	}

	slog.Debug("falling back to yaml manifest", "url", u)
	return yaml.Resolution(r.Name(), u), nil
}

// isChartRepo reports whether the URL hosts a helm chart repository, by
// fetching its index.yaml and checking for the repository's entries map.
func (r *Resolver) isChartRepo(u *url.URL) bool {
	index := *u
	index.RawQuery = ""
	index.Path = strings.TrimSuffix(u.Path, "/") + "/index.yaml"

	resp, err := r.client.Get(index.String())
	if err != nil {
		slog.Debug("chart repo probe failed", "url", &index, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}

	// index.yaml is a YAML document keyed by apiVersion and entries; a light
	// content check avoids pulling the whole (potentially large) index.
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return strings.Contains(string(head), "entries:")
}
