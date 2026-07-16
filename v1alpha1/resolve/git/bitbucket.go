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

// bitbucket resolves repositories on bitbucket.org via the Bitbucket 2.0 API.
// Bitbucket has no releases and no recursive tree endpoint, so the "latest
// release" falls back to the newest tag (then the main branch), and the tree
// is walked directory by directory.
type bitbucket struct {
	client *http.Client
}

func newBitbucket(client *http.Client) *bitbucket { return &bitbucket{client: client} }

func (b *bitbucket) detect(resource string) bool {
	return strings.HasPrefix(resource, "git@bitbucket.org:") ||
		strings.HasPrefix(resource, "https://bitbucket.org/") ||
		strings.HasPrefix(resource, "http://bitbucket.org/")
}

func (b *bitbucket) repo(resource string) (string, error) {
	slug := resource
	slug = strings.TrimPrefix(slug, "git@bitbucket.org:")
	slug = strings.TrimPrefix(slug, "https://bitbucket.org/")
	slug = strings.TrimPrefix(slug, "http://bitbucket.org/")
	slug = strings.TrimSuffix(slug, ".git")
	slug = strings.Trim(slug, "/")

	if i := strings.Index(slug, "/src/"); i >= 0 {
		slug = slug[:i]
	}
	parts := strings.Split(slug, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("git resolver: cannot determine workspace/repo from %q", resource)
	}
	return parts[0] + "/" + parts[1], nil
}

// parseRef reads Bitbucket's /src/<ref>/<path> form. Bitbucket uses the same
// path for files and directories, so isBlob is left false and Resolve decides
// from the tree.
func (b *bitbucket) parseRef(resource string) (ref, subpath string, isBlob bool) {
	if i := strings.Index(resource, "/src/"); i >= 0 {
		ref, subpath = splitRef(resource[i+len("/src/"):])
	}
	return ref, subpath, false
}

func (b *bitbucket) latestRelease(ctx context.Context, repo string) (string, error) {
	var tags struct {
		Values []struct {
			Name string `json:"name"`
		} `json:"values"`
	}
	url := "https://api.bitbucket.org/2.0/repositories/" + repo + "/refs/tags?pagelen=1&sort=-target.date"
	if _, err := getJSON(ctx, b.client, url, b.headers(), &tags); err != nil {
		return "", err
	}
	if len(tags.Values) > 0 && tags.Values[0].Name != "" {
		slog.Info("found latest tag", "repo", repo, "tag", tags.Values[0].Name)
		return tags.Values[0].Name, nil
	}

	// No tags: fall back to the repository's main branch.
	var meta struct {
		MainBranch struct {
			Name string `json:"name"`
		} `json:"mainbranch"`
	}
	if _, err := getJSON(ctx, b.client, "https://api.bitbucket.org/2.0/repositories/"+repo, b.headers(), &meta); err != nil {
		return "", err
	}
	if meta.MainBranch.Name == "" {
		return "", fmt.Errorf("git resolver: %s has no tags or main branch", repo)
	}
	slog.Info("found main branch", "repo", repo, "branch", meta.MainBranch.Name)
	return meta.MainBranch.Name, nil
}

func (b *bitbucket) tree(ctx context.Context, repo, ref string) ([]string, error) {
	var paths []string
	// Bitbucket lists one directory at a time; walk breadth-first.
	queue := []string{fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/src/%s/?pagelen=100", repo, ref)}
	for len(queue) > 0 {
		url := queue[0]
		queue = queue[1:]

		var page struct {
			Values []struct {
				Path string `json:"path"`
				Type string `json:"type"`
			} `json:"values"`
			Next string `json:"next"`
		}
		if _, err := getJSON(ctx, b.client, url, b.headers(), &page); err != nil {
			return nil, err
		}
		for _, v := range page.Values {
			switch v.Type {
			case "commit_file":
				paths = append(paths, v.Path)
			case "commit_directory":
				queue = append(queue, fmt.Sprintf("https://api.bitbucket.org/2.0/repositories/%s/src/%s/%s?pagelen=100", repo, ref, v.Path))
			}
		}
		if page.Next != "" {
			queue = append(queue, page.Next)
		}
	}
	slog.Debug("fetched repo tree", "host", "bitbucket.org", "repo", repo, "ref", ref, "files", len(paths))
	return paths, nil
}

func (b *bitbucket) rawURL(repo, ref, path string) *url.URL {
	return &url.URL{Scheme: "https", Host: "bitbucket.org", Path: "/" + repo + "/raw/" + ref + "/" + path}
}

func (b *bitbucket) headers() map[string]string {
	h := map[string]string{}
	if token := os.Getenv("BITBUCKET_TOKEN"); token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return h
}
