package kustomize

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"testing"
)

// fakeFetch serves URL paths from a map.
func fakeFetch(files map[string][]byte) Fetch {
	return func(_ context.Context, u *url.URL) ([]byte, error) {
		content, ok := files[u.Path]
		if !ok {
			return nil, fmt.Errorf("not found: %s", u)
		}
		return content, nil
	}
}

// untar returns the archive's entries by name.
func untar(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	entries := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading tar entry %s: %v", header.Name, err)
		}
		entries[header.Name] = content
	}
}

func TestMaterialize(t *testing.T) {
	base, _ := url.Parse("https://example.com/app/kustomization.yaml")
	kustomization := []byte(`resources:
  - https://example.com/remote.yaml
  - ./configmap.yaml
  - sub/dir/app.yaml
`)
	configmap := []byte("kind: ConfigMap\n")
	app := []byte("kind: Deployment\n")

	archive, buildDir, err := Materialize(context.Background(), kustomization, base, fakeFetch(map[string][]byte{
		"/app/configmap.yaml":   configmap,
		"/app/sub/dir/app.yaml": app,
	}))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if buildDir != "app" {
		t.Errorf("buildDir = %q, want %q", buildDir, "app")
	}

	entries := untar(t, archive)
	if !bytes.Equal(entries["app/kustomization.yaml"], kustomization) {
		t.Errorf("kustomization.yaml modified in flight:\n%s", entries["app/kustomization.yaml"])
	}
	if !bytes.Equal(entries["app/configmap.yaml"], configmap) {
		t.Errorf("configmap.yaml missing or wrong: %q", entries["app/configmap.yaml"])
	}
	if !bytes.Equal(entries["app/sub/dir/app.yaml"], app) {
		t.Errorf("sub/dir/app.yaml missing or wrong: %q", entries["app/sub/dir/app.yaml"])
	}
}

func TestMaterializeNested(t *testing.T) {
	base, _ := url.Parse("https://example.com/app/kustomization.yaml")
	root := []byte("resources:\n  - ./sub/\n")
	nested := []byte("resources:\n  - ./inner.yaml\n")
	inner := []byte("kind: Service\n")

	archive, _, err := Materialize(context.Background(), root, base, fakeFetch(map[string][]byte{
		"/app/sub/kustomization.yaml": nested,
		"/app/sub/inner.yaml":         inner,
	}))
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	entries := untar(t, archive)
	if !bytes.Equal(entries["app/sub/kustomization.yaml"], nested) {
		t.Errorf("nested kustomization missing: %q", entries["app/sub/kustomization.yaml"])
	}
	if !bytes.Equal(entries["app/sub/inner.yaml"], inner) {
		t.Errorf("nested resource missing: %q", entries["app/sub/inner.yaml"])
	}
}

// TestMaterializeCrossSiteRejected covers a relative resource whose ../
// chain resolves onto a different host — rejected, since it would leave the
// site the kustomization was fetched from.
func TestMaterializeCrossSiteRejected(t *testing.T) {
	base, _ := url.Parse("https://example.com/app/kustomization.yaml")
	kustomization := []byte("resources:\n  - //evil.com/evil.yaml\n")

	if _, _, err := Materialize(context.Background(), kustomization, base, fakeFetch(nil)); err == nil {
		t.Fatal("expected error for resource escaping the site")
	}
}
