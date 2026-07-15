// Package kustomize prepares URL-sourced kustomizations for the server-side
// build. A kustomization fetched from a URL may reference resources by
// relative path; those are meaningless in the builder pod's temp dir, so
// they are rebased onto the kustomization's own URL first.
package kustomize

import (
	"fmt"
	"log/slog"
	"net/url"

	"sigs.k8s.io/yaml"
)

// Rebase rewrites relative resource paths in the kustomization to absolute
// URLs resolved against base (the URL the kustomization was fetched from).
// Absolute entries are left untouched.
func Rebase(kustomization []byte, base *url.URL) ([]byte, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(kustomization, &doc); err != nil {
		return nil, fmt.Errorf("kustomize: parsing kustomization: %w", err)
	}

	resources, ok := doc["resources"].([]any)
	if !ok {
		return kustomization, nil
	}

	rebased := false
	for i, entry := range resources {
		resource, ok := entry.(string)
		if !ok {
			continue
		}
		if u, err := url.Parse(resource); err == nil && u.Scheme != "" {
			continue
		}
		absolute := base.ResolveReference(&url.URL{Path: resource}).String()
		slog.Debug("rebased relative resource", "from", resource, "to", absolute)
		resources[i] = absolute
		rebased = true
	}
	if !rebased {
		return kustomization, nil
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("kustomize: rendering kustomization: %w", err)
	}
	return out, nil
}
