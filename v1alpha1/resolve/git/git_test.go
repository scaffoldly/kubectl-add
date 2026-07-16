package git

import (
	"net/http"
	"strings"
	"testing"
)

func testClient() *http.Client { return &http.Client{} }

func TestGitHubRepo(t *testing.T) {
	g := newGitHub(testClient())
	for resource, want := range map[string]string{
		"kubernetes/ingress-nginx":                                     "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx":                  "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx.git":              "kubernetes/ingress-nginx",
		"git@github.com:kubernetes/ingress-nginx.git":                  "kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx/tree/main/charts": "kubernetes/ingress-nginx",
	} {
		got, err := g.repo(resource)
		if err != nil {
			t.Errorf("repo(%q): %v", resource, err)
			continue
		}
		if got != want {
			t.Errorf("repo(%q) = %q, want %q", resource, got, want)
		}
	}

	if _, err := g.repo("just-one-segment"); err == nil {
		t.Error("repo(\"just-one-segment\"): expected error")
	}
}

func TestGitHubParseRef(t *testing.T) {
	g := newGitHub(testClient())
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
		ref, subpath, isBlob := g.parseRef(tc.resource)
		if ref != tc.ref || subpath != tc.subpath || isBlob != tc.isBlob {
			t.Errorf("parseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.resource, ref, subpath, isBlob, tc.ref, tc.subpath, tc.isBlob)
		}
	}
}

func TestGitHubRawURL(t *testing.T) {
	got := newGitHub(testClient()).rawURL("org/repo", "main", "charts/app/Chart.yaml").String()
	want := "https://raw.githubusercontent.com/org/repo/main/charts/app/Chart.yaml"
	if got != want {
		t.Errorf("rawURL = %q, want %q", got, want)
	}
}

func TestGitLabRepo(t *testing.T) {
	g := newGitLab(testClient())
	for resource, want := range map[string]string{
		"https://gitlab.com/gitlab-org/gitlab":                      "gitlab-org/gitlab",
		"https://gitlab.com/gitlab-org/gitlab.git":                  "gitlab-org/gitlab",
		"git@gitlab.com:gitlab-org/gitlab.git":                      "gitlab-org/gitlab",
		"https://gitlab.com/group/subgroup/proj/-/tree/main/charts": "group/subgroup/proj",
		"https://gitlab.com/group/proj/-/blob/main/deploy/app.yaml": "group/proj",
	} {
		got, err := g.repo(resource)
		if err != nil {
			t.Errorf("repo(%q): %v", resource, err)
			continue
		}
		if got != want {
			t.Errorf("repo(%q) = %q, want %q", resource, got, want)
		}
	}
}

func TestGitLabParseRef(t *testing.T) {
	g := newGitLab(testClient())
	cases := []struct {
		resource string
		ref      string
		subpath  string
		isBlob   bool
	}{
		{"https://gitlab.com/org/repo/-/tree/main/charts/app", "main", "charts/app", false},
		{"https://gitlab.com/org/repo/-/blob/v1.2.3/deploy/app.yaml", "v1.2.3", "deploy/app.yaml", true},
		{"https://gitlab.com/org/repo", "", "", false},
	}
	for _, tc := range cases {
		ref, subpath, isBlob := g.parseRef(tc.resource)
		if ref != tc.ref || subpath != tc.subpath || isBlob != tc.isBlob {
			t.Errorf("parseRef(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.resource, ref, subpath, isBlob, tc.ref, tc.subpath, tc.isBlob)
		}
	}
}

func TestGitLabRawURL(t *testing.T) {
	got := newGitLab(testClient()).rawURL("org/repo", "main", "deploy/app.yaml").String()
	want := "https://gitlab.com/org/repo/-/raw/main/deploy/app.yaml"
	if got != want {
		t.Errorf("rawURL = %q, want %q", got, want)
	}
}

func TestBitbucketRepo(t *testing.T) {
	b := newBitbucket(testClient())
	for resource, want := range map[string]string{
		"https://bitbucket.org/atlassian/pipelines":                       "atlassian/pipelines",
		"https://bitbucket.org/atlassian/pipelines.git":                   "atlassian/pipelines",
		"git@bitbucket.org:atlassian/pipelines.git":                       "atlassian/pipelines",
		"https://bitbucket.org/atlassian/pipelines/src/master/charts/app": "atlassian/pipelines",
	} {
		got, err := b.repo(resource)
		if err != nil {
			t.Errorf("repo(%q): %v", resource, err)
			continue
		}
		if got != want {
			t.Errorf("repo(%q) = %q, want %q", resource, got, want)
		}
	}
}

func TestBitbucketParseRef(t *testing.T) {
	b := newBitbucket(testClient())
	cases := []struct {
		resource string
		ref      string
		subpath  string
	}{
		{"https://bitbucket.org/org/repo/src/master/charts/app", "master", "charts/app"},
		{"https://bitbucket.org/org/repo/src/v1.2.3/deploy/app.yaml", "v1.2.3", "deploy/app.yaml"},
		{"https://bitbucket.org/org/repo", "", ""},
	}
	for _, tc := range cases {
		ref, subpath, isBlob := b.parseRef(tc.resource)
		if ref != tc.ref || subpath != tc.subpath || isBlob {
			t.Errorf("parseRef(%q) = (%q, %q, %v), want (%q, %q, false)",
				tc.resource, ref, subpath, isBlob, tc.ref, tc.subpath)
		}
	}
}

func TestBitbucketRawURL(t *testing.T) {
	got := newBitbucket(testClient()).rawURL("org/repo", "master", "deploy/app.yaml").String()
	want := "https://bitbucket.org/org/repo/raw/master/deploy/app.yaml"
	if got != want {
		t.Errorf("rawURL = %q, want %q", got, want)
	}
}

func TestDetect(t *testing.T) {
	r := New()
	yes := []string{
		"kubernetes/ingress-nginx",
		"https://github.com/kubernetes/ingress-nginx",
		"https://gitlab.com/gitlab-org/gitlab",
		"git@bitbucket.org:atlassian/pipelines.git",
	}
	for _, s := range yes {
		if !r.Detect(s) {
			t.Errorf("Detect(%q) = false, want true", s)
		}
	}
	no := []string{
		"https://example.com/chart.tgz",
		"oci://registry-1.docker.io/bitnamicharts/nginx",
	}
	for _, s := range no {
		if r.Detect(s) {
			t.Errorf("Detect(%q) = true, want false", s)
		}
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
	g := newGitHub(testClient())

	tag, err := g.latestRelease("kubernetes/ingress-nginx")
	if err != nil {
		t.Fatalf("latestRelease: %v", err)
	}
	if tag == "" {
		t.Fatal("latestRelease: empty tag")
	}
	t.Logf("latest release: %s", tag)

	paths, err := g.tree("kubernetes/ingress-nginx", tag)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	found := false
	for _, p := range paths {
		if strings.HasPrefix(p, "charts/ingress-nginx") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("tree at %s: expected charts/ingress-nginx path, got %d paths", tag, len(paths))
	}
}
