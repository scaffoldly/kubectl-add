package helm

import "testing"

func TestHasTag(t *testing.T) {
	cases := map[string]bool{
		"registry-1.docker.io/bitnamicharts/nginx":         false,
		"registry-1.docker.io/bitnamicharts/nginx:25.0.14": true,
		"localhost:5000/charts/app":                        false, // host port, not a tag
		"localhost:5000/charts/app:1.2.3":                  true,
	}
	for repo, want := range cases {
		if got := hasTag(repo); got != want {
			t.Errorf("hasTag(%q) = %v, want %v", repo, got, want)
		}
	}
}

func TestHighestSemver(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"picks greatest", []string{"1.0.0", "2.5.1", "2.5.0", "1.9.9"}, "2.5.1"},
		{"v-prefixed", []string{"v1.2.0", "v1.10.0", "v1.3.0"}, "v1.10.0"},
		{"ignores non-semver", []string{"latest", "3.1.4", "stable"}, "3.1.4"},
		{"falls back to first", []string{"latest", "stable"}, "latest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := highestSemver(tc.tags); got != tc.want {
				t.Errorf("highestSemver(%v) = %q, want %q", tc.tags, got, tc.want)
			}
		})
	}
}
