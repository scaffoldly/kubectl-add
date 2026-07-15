// Package kustomize prepares URL-sourced kustomizations for the server-side
// build. A kustomization fetched from a URL may reference resources by
// relative path; those are meaningless in the builder pod's temp dir, so the
// referenced tree is materialized — every relative resource is fetched
// relative to the kustomization's URL and packed, together with the
// unmodified kustomization.yaml, into a tar the builder unpacks and builds.
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

// Materialize fetches the kustomization's relative resources into an
// in-memory tree rooted at the kustomization's URL directory and returns it
// as a tar archive. The kustomization itself is stored unmodified; absolute
// resource URLs are left for the builder to fetch during the build.
func Materialize(ctx context.Context, kustomization []byte, base *url.URL, fetch Fetch) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	if err := materialize(ctx, tw, kustomization, base, ".", fetch); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("kustomize: closing tar: %w", err)
	}
	return buf.Bytes(), nil
}

// materialize writes the kustomization at dir into the tar and recurses into
// relative resources, fetching files and nested kustomizations.
func materialize(ctx context.Context, tw *tar.Writer, kustomization []byte, base *url.URL, dir string, fetch Fetch) error {
	if err := writeFile(tw, path.Join(dir, "kustomization.yaml"), kustomization); err != nil {
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

		rel := path.Clean(resource)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("kustomize: relative resource %q escapes the kustomization root", resource)
		}

		// A directory entry is itself a kustomization.
		if strings.HasSuffix(resource, "/") || path.Ext(rel) == "" {
			nestedURL := base.ResolveReference(&url.URL{Path: rel + "/kustomization.yaml"})
			nested, err := fetch(ctx, nestedURL)
			if err != nil {
				return fmt.Errorf("kustomize: fetching nested kustomization %s: %w", nestedURL, err)
			}
			slog.Debug("materialized nested kustomization", "path", rel, "url", nestedURL)
			if err := materialize(ctx, tw, nested, nestedURL, path.Join(dir, rel), fetch); err != nil {
				return err
			}
			continue
		}

		fileURL := base.ResolveReference(&url.URL{Path: rel})
		content, err := fetch(ctx, fileURL)
		if err != nil {
			return fmt.Errorf("kustomize: fetching resource %s: %w", fileURL, err)
		}
		slog.Debug("materialized resource", "path", rel, "url", fileURL)
		if err := writeFile(tw, path.Join(dir, rel), content); err != nil {
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
