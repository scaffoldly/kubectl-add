package git

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/kustomize"
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

// Resolve sniffs the repo tree at the latest release and routes to the
// first format found: a chart under charts/ (preferring one named after
// the repo), then a root kustomization.yaml.
func (r *Resolver) Resolve(resource string) (*resolve.Resolution, error) {
	repo, err := r.Repo(resource)
	if err != nil {
		return nil, err
	}

	tag, err := r.LatestRelease(resource)
	if err != nil {
		return nil, err
	}

	paths, err := r.Tree(resource, tag)
	if err != nil {
		return nil, err
	}

	// helm: a chart under charts/, preferring charts/<repo-name>.
	name := strings.Split(repo, "/")[1]
	chart := ""
	for _, path := range paths {
		if !strings.HasPrefix(path, "charts/") || !strings.HasSuffix(path, "/Chart.yaml") {
			continue
		}
		dir := strings.TrimSuffix(path, "/Chart.yaml")
		if dir == "charts/"+name {
			chart = dir
			break
		}
		if chart == "" {
			chart = dir
		}
	}
	if chart != "" {
		slog.Info("found helm chart", "repo", repo, "chart", chart, "tag", tag)
		u, err := url.Parse(fmt.Sprintf("https://github.com/%s/tree/%s/%s", repo, tag, chart))
		if err != nil {
			return nil, fmt.Errorf("git resolver: building chart URL: %w", err)
		}
		return helm.Resolution(r.Name(), u), nil
	}

	// kustomize: kustomization.yaml at the repo root.
	for _, path := range paths {
		if path == "kustomization.yaml" {
			slog.Info("found kustomization", "repo", repo, "tag", tag)
			u, err := url.Parse(fmt.Sprintf("https://github.com/%s/tree/%s", repo, tag))
			if err != nil {
				return nil, fmt.Errorf("git resolver: building kustomization URL: %w", err)
			}
			return kustomize.Resolution(r.Name(), u), nil
		}
	}

	// TODO: yaml manifests in well-known locations (deploy/, manifests/)
	return nil, fmt.Errorf("git resolver: no installable format found in %s at %s", repo, tag)
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
		} `json:"tree"`
		// Truncated is set on huge repos; paths are then incomplete but
		// still usable for sniffing well-known locations.
		Truncated bool `json:"truncated"`
	}
	if err := r.get(fmt.Sprintf("%s/repos/%s/git/trees/%s?recursive=1", apiBase, repo, ref), &tree); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(tree.Tree))
	for _, entry := range tree.Tree {
		paths = append(paths, entry.Path)
	}
	slog.Debug("fetched repo tree", "repo", repo, "ref", ref, "paths", len(paths), "truncated", tree.Truncated)
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
