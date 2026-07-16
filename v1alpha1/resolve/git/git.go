// Package git is the git transport: it recognizes resources that live in a
// git repository and sniffs the repo tree to determine the artifact format
// (helm chart, kustomization, yaml). Hosts differ in their APIs and URL
// shapes, so each is a provider (github, gitlab, bitbucket); the format
// routing around them is shared.
package git

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	gitpath "path"
	"strings"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/kustomize"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/yaml"
)

const resolverName = "git"

// provider abstracts a git host's API and URL scheme.
type provider interface {
	// detect reports whether this host recognizes the resource.
	detect(resource string) bool
	// repo normalizes the resource to its "org/repo" (or "workspace/repo").
	repo(resource string) (string, error)
	// parseRef extracts the ref and subpath from a full URL; empty ref means
	// "use the latest release". isBlob marks a single-file reference.
	parseRef(resource string) (ref, subpath string, isBlob bool)
	// latestRelease returns the repo's newest release tag (or newest tag /
	// default branch for hosts without releases).
	latestRelease(repo string) (string, error)
	// tree lists the repo's file paths (blobs only) at a ref.
	tree(repo, ref string) ([]string, error)
	// rawURL addresses a file at a ref for direct fetching.
	rawURL(repo, ref, path string) *url.URL
}

// Resolver dispatches a git reference to the provider for its host.
type Resolver struct {
	providers []provider
}

func New() *Resolver {
	client := &http.Client{Timeout: 30 * time.Second}
	return &Resolver{providers: []provider{
		newGitHub(client),
		newGitLab(client),
		newBitbucket(client),
	}}
}

func (r *Resolver) Name() string { return resolverName }

// Detect reports whether any provider recognizes the resource.
func (r *Resolver) Detect(resource string) bool {
	return r.provider(resource) != nil
}

func (r *Resolver) provider(resource string) provider {
	for _, p := range r.providers {
		if p.detect(resource) {
			return p
		}
	}
	return nil
}

// Resolve routes a git reference to an installable artifact, honoring an
// explicit ref and subpath from a full URL and otherwise sniffing the repo
// tree at the latest release. Artifacts are addressed by the host's raw URL
// so their contents (and, for charts, their full file set) can be fetched.
func (r *Resolver) Resolve(resource string) (*resolve.Resolution, error) {
	p := r.provider(resource)
	if p == nil {
		return nil, fmt.Errorf("git resolver: no provider recognizes %q", resource)
	}

	repo, err := p.repo(resource)
	if err != nil {
		return nil, err
	}

	ref, subpath, isBlob := p.parseRef(resource)
	if ref == "" {
		if ref, err = p.latestRelease(repo); err != nil {
			return nil, err
		}
	}

	if isBlob {
		slog.Info("found file", "repo", repo, "ref", ref, "path", subpath)
		return fileResolution(p, repo, ref, subpath)
	}

	paths, err := p.tree(repo, ref)
	if err != nil {
		return nil, err
	}

	// A subpath may itself be a file (some hosts can't tell from the URL).
	if subpath != "" {
		if contains(paths, subpath) {
			slog.Info("found file", "repo", repo, "ref", ref, "path", subpath)
			return fileResolution(p, repo, ref, subpath)
		}
		if contains(paths, subpath+"/Chart.yaml") {
			return chartResolution(p, repo, ref, subpath, paths), nil
		}
		if base, ok := kustomizationIn(paths, subpath); ok {
			slog.Info("found kustomization", "repo", repo, "ref", ref, "dir", subpath)
			return kustomize.Resolution(resolverName, p.rawURL(repo, ref, subpath+"/"+base)), nil
		}
		return nil, fmt.Errorf("git resolver: no installable format in %s/%s at %s", repo, subpath, ref)
	}

	// Bare repo: a chart under charts/ (preferring charts/<repo-name>), else
	// a kustomization at the repo root.
	name := repo[strings.LastIndex(repo, "/")+1:]
	if chart := findChart(paths, name); chart != "" {
		return chartResolution(p, repo, ref, chart, paths), nil
	}
	if base, ok := kustomizationIn(paths, ""); ok {
		slog.Info("found kustomization", "repo", repo, "ref", ref)
		return kustomize.Resolution(resolverName, p.rawURL(repo, ref, base)), nil
	}

	// TODO: yaml manifests in well-known locations (deploy/, manifests/)
	return nil, fmt.Errorf("git resolver: no installable format found in %s at %s", repo, ref)
}

// splitRef splits "<ref>/<subpath>" from a full URL's tree/blob tail.
func splitRef(rest string) (ref, subpath string) {
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

// fileResolution routes a single file by its basename.
func fileResolution(p provider, repo, ref, path string) (*resolve.Resolution, error) {
	u := p.rawURL(repo, ref, path)
	switch base := gitpath.Base(path); {
	case base == "Chart.yaml" || base == "Chart.yml":
		return helm.Resolution(resolverName, u), nil
	case base == "kustomization.yaml" || base == "kustomization.yml" || base == "Kustomization":
		return kustomize.Resolution(resolverName, u), nil
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		return yaml.Resolution(resolverName, u), nil
	default:
		return nil, fmt.Errorf("git resolver: unsupported file %q", path)
	}
}

// chartResolution builds a helm Resolution for the chart at dir, addressing
// its Chart.yaml by raw URL and listing the chart's member files (relative to
// dir) so discovery fetches the real file set rather than guessing.
func chartResolution(p provider, repo, ref, dir string, paths []string) *resolve.Resolution {
	slog.Info("found helm chart", "repo", repo, "chart", dir, "ref", ref)
	res := helm.Resolution(resolverName, p.rawURL(repo, ref, dir+"/Chart.yaml"))

	prefix := dir + "/"
	for _, path := range paths {
		rel := strings.TrimPrefix(path, prefix)
		if rel != path && rel != "Chart.yaml" && isChartFile(rel) {
			res.Files = append(res.Files, rel)
		}
	}
	return res
}

// isChartFile reports whether a chart-relative path is one helm renders, so
// discovery skips docs/changelogs and only fetches what the chart needs.
func isChartFile(rel string) bool {
	switch rel {
	case "values.yaml", "values.schema.json", "Chart.lock", ".helmignore":
		return true
	}
	if strings.HasSuffix(rel, ".tpl") {
		return true
	}
	return strings.HasPrefix(rel, "templates/") ||
		strings.HasPrefix(rel, "charts/") ||
		strings.HasPrefix(rel, "crds/")
}

// findChart returns the chart directory under charts/, preferring
// charts/<name>, or "" when none is found.
func findChart(paths []string, name string) string {
	chart := ""
	for _, path := range paths {
		if !strings.HasPrefix(path, "charts/") || !strings.HasSuffix(path, "/Chart.yaml") {
			continue
		}
		dir := strings.TrimSuffix(path, "/Chart.yaml")
		if dir == "charts/"+name {
			return dir
		}
		if chart == "" {
			chart = dir
		}
	}
	return chart
}

// kustomizationIn reports the kustomization basename found directly in dir
// ("" for the repo root), if any.
func kustomizationIn(paths []string, dir string) (string, bool) {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	for _, base := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if contains(paths, prefix+base) {
			return base, true
		}
	}
	return "", false
}

func contains(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

// isShorthand reports whether resource is the bare "org/repo" form (no
// scheme, exactly two non-empty segments).
func isShorthand(resource string) bool {
	if strings.Contains(resource, "://") || strings.Contains(resource, "@") {
		return false
	}
	parts := strings.Split(resource, "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}

// getJSON performs a GET and decodes a JSON response, applying headers (e.g.
// auth). It returns the response so callers can read pagination headers.
func getJSON(client *http.Client, rawURL string, headers map[string]string, out any) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("git resolver: building request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("git resolver: %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return resp, fmt.Errorf("git resolver: %s: %s", rawURL, resp.Status)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp, fmt.Errorf("git resolver: decoding %s: %w", rawURL, err)
		}
	}
	return resp, nil
}
