package git

import (
	"strings"
	"testing"
)

func TestRepo(t *testing.T) {
	b := New()
	for resource, want := range map[string]string{
		"kubernetes/ingress-nginx":                                     "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx":                  "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx.git":              "kubernetes/ingress-nginx",
		"git@github.com:kubernetes/ingress-nginx.git":                  "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx/tree/main/charts": "kubernetes/ingress-nginx",
	} {
		got, err := b.Repo(resource)
		if err != nil {
			t.Errorf("Repo(%q): %v", resource, err)
			continue
		}
		if got != want {
			t.Errorf("Repo(%q) = %q, want %q", resource, got, want)
		}
	}

	if _, err := b.Repo("just-one-segment"); err == nil {
		t.Error("Repo(\"just-one-segment\"): expected error")
	}
}

func TestLatestReleaseAndTree(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	b := New()

	tag, err := b.LatestRelease("kubernetes/ingress-nginx")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if tag == "" {
		t.Fatal("LatestRelease: empty tag")
	}
	t.Logf("latest release: %s", tag)

	paths, err := b.Tree("kubernetes/ingress-nginx", tag)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	found := false
	for _, p := range paths {
		if strings.HasPrefix(p, "charts/ingress-nginx") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Tree at %s: expected charts/ingress-nginx path, got %d paths", tag, len(paths))
	}
}
