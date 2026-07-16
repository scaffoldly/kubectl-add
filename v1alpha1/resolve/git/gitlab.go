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

// gitlab resolves repositories on gitlab.com via the GitLab v4 API. The
// project path is URL-encoded ("org/repo" -> "org%2Frepo").
type gitlab struct {
	client *http.Client
}

func newGitLab(client *http.Client) *gitlab { return &gitlab{client: client} }

func (g *gitlab) detect(resource string) bool {
	return strings.HasPrefix(resource, "git@gitlab.com:") ||
		strings.HasPrefix(resource, "https://gitlab.com/") ||
		strings.HasPrefix(resource, "http://gitlab.com/")
}

func (g *gitlab) repo(resource string) (string, error) {
	slug := resource
	slug = strings.TrimPrefix(slug, "git@gitlab.com:")
	slug = strings.TrimPrefix(slug, "https://gitlab.com/")
	slug = strings.TrimPrefix(slug, "http://gitlab.com/")
	slug = strings.TrimSuffix(slug, ".git")
	slug = strings.Trim(slug, "/")

	// Drop the /-/tree|blob/... tail; GitLab paths can nest subgroups, so the
	// project is everything before "/-/".
	if i := strings.Index(slug, "/-/"); i >= 0 {
		slug = slug[:i]
	}
	parts := strings.Split(slug, "/")
	if len(parts) < 2 || parts[0] == "" || parts[len(parts)-1] == "" {
		return "", fmt.Errorf("git resolver: cannot determine project from %q", resource)
	}
	return slug, nil
}

func (g *gitlab) parseRef(resource string) (ref, subpath string, isBlob bool) {
	if i := strings.Index(resource, "/-/blob/"); i >= 0 {
		ref, subpath = splitRef(resource[i+len("/-/blob/"):])
		return ref, subpath, true
	}
	if i := strings.Index(resource, "/-/tree/"); i >= 0 {
		ref, subpath = splitRef(resource[i+len("/-/tree/"):])
		return ref, subpath, false
	}
	return "", "", false
}

func (g *gitlab) latestRelease(ctx context.Context, repo string) (string, error) {
	var releases []struct {
		TagName string `json:"tag_name"`
	}
	url := "https://gitlab.com/api/v4/projects/" + g.projectID(repo) + "/releases?per_page=1"
	if _, err := getJSON(ctx, g.client, url, g.headers(), &releases); err != nil {
		return "", err
	}
	if len(releases) == 0 || releases[0].TagName == "" {
		return "", fmt.Errorf("git resolver: %s has no releases", repo)
	}
	slog.Info("found latest release", "repo", repo, "tag", releases[0].TagName)
	return releases[0].TagName, nil
}

func (g *gitlab) tree(ctx context.Context, repo, ref string) ([]string, error) {
	var paths []string
	// The tree endpoint is paginated; follow X-Next-Page until exhausted.
	for page := "1"; page != ""; {
		var entries []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		}
		url := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/repository/tree?recursive=true&ref=%s&per_page=100&page=%s",
			g.projectID(repo), ref, page)
		resp, err := getJSON(ctx, g.client, url, g.headers(), &entries)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type == "blob" {
				paths = append(paths, e.Path)
			}
		}
		page = resp.Header.Get("X-Next-Page")
	}
	slog.Debug("fetched repo tree", "host", "gitlab.com", "repo", repo, "ref", ref, "files", len(paths))
	return paths, nil
}

func (g *gitlab) rawURL(repo, ref, path string) *url.URL {
	return &url.URL{Scheme: "https", Host: "gitlab.com", Path: "/" + repo + "/-/raw/" + ref + "/" + path}
}

// projectID is the URL-encoded project path GitLab's API keys on.
func (g *gitlab) projectID(repo string) string { return url.PathEscape(repo) }

func (g *gitlab) headers() map[string]string {
	h := map[string]string{}
	if token := os.Getenv("GITLAB_TOKEN"); token != "" {
		h["PRIVATE-TOKEN"] = token
	}
	return h
}
