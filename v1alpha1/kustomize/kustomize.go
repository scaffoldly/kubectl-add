// Package kustomize prepares URL-sourced kustomizations for the server-side
// build. A kustomization fetched from a URL may reference resources by
// relative path — including in-site ../ paths into sibling directories —
// which are meaningless in the builder pod's temp dir. The referenced tree
// is materialized: every relative resource is fetched relative to the
// kustomization's URL and packed, mirroring the URL path structure, into a
// tar the builder unpacks and builds. url.ResolveReference clamps ../ at the
// host root, so a relative path can never escape the site.
package kustomize

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"

	"sigs.k8s.io/yaml"
)

// Fetch retrieves the content at u.
type Fetch func(ctx context.Context, u *url.URL) ([]byte, error)

// Materialize fetches the kustomization's relative resources into a tar
// archive that mirrors their URL paths (rooted at the host). It returns the
// archive and the directory within it that holds the kustomization, which
// the builder passes to `kubectl kustomize`. Absolute resource URLs are left
// for the builder to fetch during the build.
func Materialize(ctx context.Context, kustomization []byte, base *url.URL, fetch Fetch) (archive []byte, buildDir string, err error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := materialize(ctx, tw, kustomization, base, fetch, map[string]bool{}); err != nil {
		return nil, "", err
	}
	if err := tw.Close(); err != nil {
		return nil, "", fmt.Errorf("kustomize: closing tar: %w", err)
	}

	return buf.Bytes(), path.Dir(strings.TrimPrefix(base.Path, "/")), nil
}

// materialize writes the kustomization at base into the tar under its
// host-relative path and recurses into relative resources, fetching files
// and nested kustomizations.
func materialize(ctx context.Context, tw *tar.Writer, kustomization []byte, base *url.URL, fetch Fetch, seen map[string]bool) error {
	name := strings.TrimPrefix(base.Path, "/")
	if seen[name] {
		return nil
	}
	seen[name] = true
	if err := writeFile(tw, name, kustomization); err != nil {
		return err
	}

	var doc struct {
		Resources []string `json:"resources"`
	}
	if err := yaml.Unmarshal(kustomization, &doc); err != nil {
		return fmt.Errorf("kustomize: parsing kustomization at %s: %w", base, err)
	}

	for _, resource := range doc.Resources {
		if u, err := url.Parse(resource); err == nil && u.Scheme != "" {
			// Absolute: the builder fetches it during the build.
			continue
		}

		resolved := base.ResolveReference(&url.URL{Path: resource})
		if resolved.Host != base.Host {
			return fmt.Errorf("kustomize: relative resource %q escapes the site", resource)
		}

		// A directory entry is itself a kustomization.
		if strings.HasSuffix(resource, "/") || path.Ext(resolved.Path) == "" {
			nested := *resolved
			nested.Path = path.Join(resolved.Path, "kustomization.yaml")
			content, err := fetch(ctx, &nested)
			if err != nil {
				return fmt.Errorf("kustomize: fetching nested kustomization %s: %w", &nested, err)
			}
			slog.Debug("materialized nested kustomization", "url", &nested)
			if err := materialize(ctx, tw, content, &nested, fetch, seen); err != nil {
				return err
			}
			continue
		}

		content, err := fetch(ctx, resolved)
		if err != nil {
			return fmt.Errorf("kustomize: fetching resource %s: %w", resolved, err)
		}
		slog.Debug("materialized resource", "url", resolved)
		if err := writeFile(tw, strings.TrimPrefix(resolved.Path, "/"), content); err != nil {
			return err
		}
	}
	return nil
}

func writeFile(tw *tar.Writer, name string, content []byte) error {
	header := &tar.Header{
		Name: name,
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("kustomize: writing tar header %s: %w", name, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("kustomize: writing tar entry %s: %w", name, err)
	}
	return nil
}
