package http

import (
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/kustomize"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/yaml"
)

// Resolver is the http transport: it recognizes resources reachable over
// http(s) and sniffs their content to determine the artifact format. It is
// the fallback transport: anything it can't identify is treated as a yaml
// manifest.
type Resolver struct{}

func New() *Resolver {
	return &Resolver{}
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

	// TODO: ContentType sniff for chart repos (index.yaml) before falling
	// back to yaml.
	slog.Debug("falling back to yaml manifest", "url", u)
	return yaml.Resolution(r.Name(), u), nil
}

// ContentType sniffs the resource's content type (HEAD request), for
// format detection (yaml manifest, chart tarball, helm repo index).
func (r *Resolver) ContentType(resource string) (string, error) {
	// TODO: HEAD the URL and return Content-Type
	return "", fmt.Errorf("http resolver: ContentType not implemented")
}

// Get fetches the resource's content.
func (r *Resolver) Get(resource string) ([]byte, error) {
	// TODO: GET the URL
	return nil, fmt.Errorf("http resolver: Get not implemented")
}
