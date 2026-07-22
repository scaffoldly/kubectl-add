package tunnel

import "testing"

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in        string
		wantName  string
		wantIsSvc bool
	}{
		{"", "", false},
		{"kubernetes", "", false},
		{"svc/kubernetes", "", false},
		{"service/kubernetes", "", false},
		{"  kubernetes  ", "", false},
		{"foo", "foo", true},
		{"svc/foo", "foo", true},
		{"service/foo", "foo", true},
		{"services/foo", "foo", true},
	}
	for _, c := range cases {
		name, isSvc := parseTarget(c.in)
		if name != c.wantName || isSvc != c.wantIsSvc {
			t.Errorf("parseTarget(%q) = (%q, %v), want (%q, %v)", c.in, name, isSvc, c.wantName, c.wantIsSvc)
		}
	}
}

func TestSingleJoiningSlash(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"/api/v1/namespaces/x/services/y/proxy", "/pods", "/api/v1/namespaces/x/services/y/proxy/pods"},
		{"/prefix/", "/path", "/prefix/path"},
		{"/prefix", "path", "/prefix/path"},
		{"/prefix/", "path", "/prefix/path"},
		{"/prefix", "", "/prefix/"},
	}
	for _, c := range cases {
		if got := singleJoiningSlash(c.a, c.b); got != c.want {
			t.Errorf("singleJoiningSlash(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
