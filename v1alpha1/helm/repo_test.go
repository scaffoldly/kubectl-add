package helm

import (
	"net/url"
	"testing"

	"helm.sh/helm/v3/pkg/repo"
)

func index(names ...string) *repo.IndexFile {
	idx := &repo.IndexFile{Entries: map[string]repo.ChartVersions{}}
	for _, n := range names {
		idx.Entries[n] = repo.ChartVersions{}
	}
	return idx
}

func TestChooseChart(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		idx     *repo.IndexFile
		want    string
		wantErr bool
	}{
		{"name match", "https://metallb.github.io/metallb", index("metallb", "other"), "metallb", false},
		{"trailing slash match", "https://metallb.github.io/metallb/", index("metallb"), "metallb", false},
		{"sole entry", "https://example.com/charts", index("only"), "only", false},
		{"ambiguous", "https://example.com/charts", index("a", "b"), "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := url.Parse(tc.repo)
			got, err := chooseChart(tc.idx, u)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("chooseChart: %v", err)
			}
			if got != tc.want {
				t.Errorf("chooseChart = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChartURL(t *testing.T) {
	repoURL, _ := url.Parse("https://charts.example.com/stable")
	cases := []struct {
		name string
		ref  string
		want string
	}{
		{"absolute", "https://cdn.example.com/hello-1.0.0.tgz", "https://cdn.example.com/hello-1.0.0.tgz"},
		{"repo-relative", "hello-1.0.0.tgz", "https://charts.example.com/stable/hello-1.0.0.tgz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chartURL(repoURL, tc.ref)
			if err != nil {
				t.Fatalf("chartURL: %v", err)
			}
			if got.String() != tc.want {
				t.Errorf("chartURL = %q, want %q", got, tc.want)
			}
		})
	}
}
