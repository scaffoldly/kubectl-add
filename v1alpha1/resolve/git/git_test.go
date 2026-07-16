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

func TestParseRef(t *testing.T) {
	cases := []struct {
		resource string
		ref      string
		subpath  string
		isBlob   bool
	}{
		{"kubernetes/ingress-nginx", "", "", false},
		{"https://github.com/kubernetes/ingress-nginx", "", "", false},
		{"https://github.com/kubernetes/ingress-nginx.git", "", "", false},
		{"https://github.com/org/repo/tree/main", "main", "", false},
		{"https://github.com/org/repo/tree/main/charts/app", "main", "charts/app", false},
		{"https://github.com/org/repo/tree/v1.2.3/charts/app/", "v1.2.3", "charts/app", false},
		{"https://github.com/org/repo/blob/main/deploy/app.yaml", "main", "deploy/app.yaml", true},
	}
	for _, tc := range cases {
		ref, subpath, isBlob := parseRef(tc.resource)
		if ref != tc.ref || subpath != tc.subpath || isBlob != tc.isBlob {
			t.Errorf("parseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.resource, ref, subpath, isBlob, tc.ref, tc.subpath, tc.isBlob)
		}
	}
}

func TestRawURL(t *testing.T) {
	got := New().rawURL("org/repo", "main", "charts/app/Chart.yaml").String()
	want := "https://raw.githubusercontent.com/org/repo/main/charts/app/Chart.yaml"
	if got != want {
		t.Errorf("rawURL = %q, want %q", got, want)
	}
}

func TestIsChartFile(t *testing.T) {
	want := map[string]bool{
		"values.yaml":                 true,
		"values.schema.json":          true,
		".helmignore":                 true,
		"templates/_helpers.tpl":      true,
		"templates/deployment.yaml":   true,
		"charts/subchart/Chart.yaml":  true,
		"crds/crd.yaml":               true,
		"README.md":                   false,
		"OWNERS":                      false,
		"changelog/helm-chart-1.0.md": false,
	}
	for rel, exp := range want {
		if got := isChartFile(rel); got != exp {
			t.Errorf("isChartFile(%q) = %v, want %v", rel, got, exp)
		}
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
