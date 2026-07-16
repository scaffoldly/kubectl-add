package git

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// github resolves repositories on github.com. It also claims the bare
// "org/repo" shorthand, defaulting it to github.com.
type github struct {
	client *http.Client
}

func newGitHub(client *http.Client) *github { return &github{client: client} }

func (g *github) detect(resource string) bool {
	switch {
	case strings.HasPrefix(resource, "git@github.com:"):
		return true
	case strings.HasPrefix(resource, "https://github.com/"), strings.HasPrefix(resource, "http://github.com/"):
		return true
	case isShorthand(resource):
		return true
	}
	return false
}

func (g *github) repo(resource string) (string, error) {
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

func (g *github) parseRef(resource string) (ref, subpath string, isBlob bool) {
	if i := strings.Index(resource, "/blob/"); i >= 0 {
		ref, subpath = splitRef(resource[i+len("/blob/"):])
		return ref, subpath, true
	}
	if i := strings.Index(resource, "/tree/"); i >= 0 {
		ref, subpath = splitRef(resource[i+len("/tree/"):])
		return ref, subpath, false
	}
	return "", "", false
}

func (g *github) latestRelease(ctx context.Context, repo string) (string, error) {
	var release struct {
		TagName string `json:"tag_name"`
	}
	if _, err := getJSON(ctx, g.client, "https://api.github.com/repos/"+repo+"/releases/latest", g.headers(), &release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("git resolver: %s has no releases", repo)
	}
	slog.Info("found latest release", "repo", repo, "tag", release.TagName)
	return release.TagName, nil
}

func (g *github) tree(ctx context.Context, repo, ref string) ([]string, error) {
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
		Truncated bool `json:"truncated"`
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, ref)
	if _, err := getJSON(ctx, g.client, url, g.headers(), &tree); err != nil {
		return nil, err
	}

	paths := make([]string, 0, len(tree.Tree))
	for _, entry := range tree.Tree {
		if entry.Type == "blob" {
			paths = append(paths, entry.Path)
		}
	}
	slog.Debug("fetched repo tree", "host", "github.com", "repo", repo, "ref", ref, "files", len(paths), "truncated", tree.Truncated)
	return paths, nil
}

func (g *github) rawURL(repo, ref, path string) *url.URL {
	return &url.URL{Scheme: "https", Host: "raw.githubusercontent.com", Path: "/" + repo + "/" + ref + "/" + path}
}

// headers sets the GitHub API accept header and, when GITHUB_TOKEN is set,
// authenticates to lift the unauthenticated rate limit.
func (g *github) headers() map[string]string {
	h := map[string]string{"Accept": "application/vnd.github+json"}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}
