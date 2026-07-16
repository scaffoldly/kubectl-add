package git

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	gitpath "path"
	"strings"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/kustomize"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/yaml"
)

const apiBase = "https://api.github.com"

// Resolver is the git transport: it recognizes resources that live in a
// git repository and sniffs the repo tree at the latest release to
// determine the artifact format (helm chart, kustomization, yaml).
// Handles full URLs (https://github.com/org/repo, git@...), and the bare
// "org/repo" shorthand, which defaults to github.com.
type Resolver struct {
	client *http.Client
}

func New() *Resolver {
	return &Resolver{
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (r *Resolver) Name() string {
	return "git"
}

// Detect reports whether the resource is a git repository reference.
func (r *Resolver) Detect(resource string) bool {
	if strings.HasPrefix(resource, "git@") || strings.HasSuffix(resource, ".git") {
		return true
	}
	if strings.HasPrefix(resource, "https://github.com/") || strings.HasPrefix(resource, "http://github.com/") {
		return true
	}
	// Bare "org/repo" shorthand: no scheme, exactly two path segments.
	if !strings.Contains(resource, "://") {
		if parts := strings.Split(resource, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return true
		}
	}
	return false
}

// Resolve routes a git reference to an installable artifact, honoring an
// explicit ref and subpath from a full GitHub tree/blob URL and otherwise
// sniffing the repo tree at the latest release. Resolved artifacts are
// addressed by raw.githubusercontent.com URLs so their contents (and, for
// charts, their full file set) can be fetched directly.
func (r *Resolver) Resolve(resource string) (*resolve.Resolution, error) {
	repo, err := r.Repo(resource)
	if err != nil {
		return nil, err
	}

	ref, subpath, isBlob := parseRef(resource)
	if ref == "" {
		if ref, err = r.LatestRelease(resource); err != nil {
			return nil, err
		}
	}

	// A blob URL points at a single file; route it by basename.
	if isBlob {
		slog.Info("found file", "repo", repo, "ref", ref, "path", subpath)
		return r.fileResolution(repo, ref, subpath)
	}

	paths, err := r.Tree(resource, ref)
	if err != nil {
		return nil, err
	}

	// A tree URL with a subpath: install what lives in that directory.
	if subpath != "" {
		if contains(paths, subpath+"/Chart.yaml") {
			return r.chartResolution(repo, ref, subpath, paths), nil
		}
		for _, base := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
			if contains(paths, subpath+"/"+base) {
				slog.Info("found kustomization", "repo", repo, "ref", ref, "dir", subpath)
				return kustomize.Resolution(r.Name(), r.rawURL(repo, ref, subpath+"/"+base)), nil
			}
		}
		return nil, fmt.Errorf("git resolver: no installable format in %s/%s at %s", repo, subpath, ref)
	}

	// Bare repo: a chart under charts/ (preferring charts/<repo-name>),
	// else a kustomization at the repo root.
	name := strings.Split(repo, "/")[1]
	if chart := findChart(paths, name); chart != "" {
		return r.chartResolution(repo, ref, chart, paths), nil
	}
	for _, base := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
		if contains(paths, base) {
			slog.Info("found kustomization", "repo", repo, "ref", ref)
			return kustomize.Resolution(r.Name(), r.rawURL(repo, ref, base)), nil
		}
	}

	// TODO: yaml manifests in well-known locations (deploy/, manifests/)
	return nil, fmt.Errorf("git resolver: no installable format found in %s at %s", repo, ref)
}

// parseRef extracts the ref and subpath from a full GitHub tree/blob URL
// (github.com/org/repo/tree/<ref>/<subpath> or .../blob/<ref>/<path>). It
// returns empty values for bare repo references (org/repo, .git, git@),
// leaving the ref to default to the latest release. A branch name containing
// slashes is not disambiguated — the first segment is taken as the ref.
func parseRef(resource string) (ref, subpath string, isBlob bool) {
	for _, marker := range []struct {
		sep  string
		blob bool
	}{{"/tree/", false}, {"/blob/", true}} {
		i := strings.Index(resource, marker.sep)
		if i < 0 {
			continue
		}
		rest := strings.Trim(resource[i+len(marker.sep):], "/")
		if rest == "" {
			return "", "", false
		}
		parts := strings.SplitN(rest, "/", 2)
		ref = parts[0]
		if len(parts) == 2 {
			subpath = parts[1]
		}
		return ref, subpath, marker.blob
	}
	return "", "", false
}

// fileResolution routes a single file (from a blob URL) by its basename.
func (r *Resolver) fileResolution(repo, ref, path string) (*resolve.Resolution, error) {
	u := r.rawURL(repo, ref, path)
	switch base := gitpath.Base(path); {
	case base == "Chart.yaml" || base == "Chart.yml":
		return helm.Resolution(r.Name(), u), nil
	case base == "kustomization.yaml" || base == "kustomization.yml" || base == "Kustomization":
		return kustomize.Resolution(r.Name(), u), nil
	case strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"):
		return yaml.Resolution(r.Name(), u), nil
	default:
		return nil, fmt.Errorf("git resolver: unsupported file %q", path)
	}
}

// chartResolution builds a helm Resolution for the chart at dir, addressing
// its Chart.yaml by raw URL and listing the chart's member files (relative to
// dir) so discovery fetches the real file set rather than guessing.
func (r *Resolver) chartResolution(repo, ref, dir string, paths []string) *resolve.Resolution {
	slog.Info("found helm chart", "repo", repo, "chart", dir, "ref", ref)
	res := helm.Resolution(r.Name(), r.rawURL(repo, ref, dir+"/Chart.yaml"))

	prefix := dir + "/"
	for _, p := range paths {
		rel := strings.TrimPrefix(p, prefix)
		if rel != p && rel != "Chart.yaml" && isChartFile(rel) {
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

// rawURL builds a raw.githubusercontent.com URL for a file at a ref.
func (r *Resolver) rawURL(repo, ref, path string) *url.URL {
	return &url.URL{
		Scheme: "https",
		Host:   "raw.githubusercontent.com",
		Path:   "/" + repo + "/" + ref + "/" + path,
	}
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

func contains(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

// Repo normalizes a detected resource into its "org/repo" slug. Extra path
// segments (e.g. .../tree/main/charts) are dropped.
func (r *Resolver) Repo(resource string) (string, error) {
	slug := resource
	slug = strings.TrimPrefix(slug, "git@github.com:")
	slug = strings.TrimPrefix(slug, "https://github.com/")
	slug = strings.TrimPrefix(slug, "http://github.com/")
	slug = strings.TrimSuffix(slug, ".git")
	slug = strings.Trim(slug, "/")

	parts := strings.Split(slug, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("git resolver: cannot determine org/repo from %q", resource)
	}
	return parts[0] + "/" + parts[1], nil
}

// LatestRelease returns the tag of the repo's latest release.
func (r *Resolver) LatestRelease(resource string) (string, error) {
	repo, err := r.Repo(resource)
	if err != nil {
		return "", err
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := r.get(fmt.Sprintf("%s/repos/%s/releases/latest", apiBase, repo), &release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("git resolver: %s has no releases", repo)
	}
	slog.Info("found latest release", "repo", repo, "tag", release.TagName)
	return release.TagName, nil
}

// Tree lists the repo's paths at a ref (tag, branch, or SHA), for format
// sniffing (charts/, kustomization.yaml, *.yaml).
func (r *Resolver) Tree(resource string, ref string) ([]string, error) {
	repo, err := r.Repo(resource)
	if err != nil {
		return nil, err
	}

	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		// Truncated is set on huge repos; paths are then incomplete but
		// still usable for sniffing well-known locations.
		Truncated bool `json:"truncated"`
	}
	if err := r.get(fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", apiBase, repo, ref), &tree); err != nil {
		return nil, err
	}

	// Only blobs (files); directory entries would be fetched as files and
	// 404 during discovery.
	paths := make([]string, 0, len(tree.Tree))
	for _, entry := range tree.Tree {
		if entry.Type == "blob" {
			paths = append(paths, entry.Path)
		}
	}
	slog.Debug("fetched repo tree", "repo", repo, "ref", ref, "files", len(paths), "truncated", tree.Truncated)
	return paths, nil
}

// get performs a GitHub API GET, decoding the JSON response into out.
// GITHUB_TOKEN is used when set to lift the unauthenticated rate limit.
func (r *Resolver) get(url string, out any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("git resolver: building request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	token := os.Getenv("GITHUB_TOKEN")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	slog.Debug("github api request", "url", url, "authenticated", token != "")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("git resolver: %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("git resolver: %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("git resolver: decoding %s: %w", url, err)
	}
	return nil
}
