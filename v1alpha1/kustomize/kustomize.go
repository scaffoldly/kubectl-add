// Package kustomize builds a kustomization into plain YAML in-process, so
// the result can be streamed to the server-side apply like any other
// manifest. Remote resources referenced by absolute URL are fetched during
// the build.
package kustomize

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Build renders the given kustomization.yaml into a multi-document YAML
// manifest. The kustomization is written to a temporary root; resources
// referenced by absolute URL are pulled by kustomize during the build.
func Build(kustomization []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "kubectl-add-kustomize")
	if err != nil {
		return nil, fmt.Errorf("kustomize: temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	if err := os.WriteFile(filepath.Join(dir, "kustomization.yaml"), kustomization, 0o600); err != nil {
		return nil, fmt.Errorf("kustomize: writing kustomization: %w", err)
	}

	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	resMap, err := k.Run(filesys.MakeFsOnDisk(), dir)
	if err != nil {
		return nil, fmt.Errorf("kustomize: build: %w", err)
	}

	yml, err := resMap.AsYaml()
	if err != nil {
		return nil, fmt.Errorf("kustomize: rendering yaml: %w", err)
	}
	return yml, nil
}
