package kustomize

import (
	"net/url"
	"strings"
	"testing"
)

func TestRebase(t *testing.T) {
	base, _ := url.Parse("https://scaffoldly.github.io/kubectl-add/kustomization.yaml")

	in := []byte(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://scaffoldly.github.io/kubectl-add/nginx.yaml
  - ./configmap.yaml
  - sub/dir/app.yaml
`)

	out, err := Rebase(in, base)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		"https://scaffoldly.github.io/kubectl-add/nginx.yaml",
		"https://scaffoldly.github.io/kubectl-add/configmap.yaml",
		"https://scaffoldly.github.io/kubectl-add/sub/dir/app.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "./configmap.yaml") {
		t.Errorf("relative path survived:\n%s", got)
	}
}

func TestRebaseNoResources(t *testing.T) {
	base, _ := url.Parse("https://example.com/kustomization.yaml")
	in := []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
	out, err := Rebase(in, base)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("expected unchanged output, got:\n%s", out)
	}
}

func TestRebaseAllAbsolute(t *testing.T) {
	base, _ := url.Parse("https://example.com/kustomization.yaml")
	in := []byte(`resources:
  - https://example.com/a.yaml
`)
	out, err := Rebase(in, base)
	if err != nil {
		t.Fatalf("Rebase: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("expected byte-identical passthrough, got:\n%s", out)
	}
}
