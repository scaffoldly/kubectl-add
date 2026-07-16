package helm

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

func fakeFetch(files map[string][]byte) Fetch {
	return func(_ context.Context, u *url.URL) ([]byte, bool, error) {
		content, ok := files[u.Path]
		return content, ok, nil
	}
}

func TestDiscoverAndRender(t *testing.T) {
	chartURL, _ := url.Parse("https://example.com/chart/Chart.yaml")
	files := map[string][]byte{
		"/chart/Chart.yaml": []byte("apiVersion: v2\nname: hello\nversion: 0.1.0\n"),
		"/chart/values.yaml": []byte("message: default hello\n"),
		"/chart/templates/configmap.yaml": []byte(
			"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: {{ .Release.Name }}-cfg\ndata:\n  message: {{ .Values.message | quote }}\n"),
	}

	chart, err := Discover(context.Background(), chartURL, nil, fakeFetch(files))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := string(chart.DefaultValues); !strings.Contains(got, "default hello") {
		t.Errorf("default values = %q", got)
	}

	// Default values.
	out, err := Render(chart.Chart, chart.DefaultValues, "rel", "ns", "v1.30.0")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(string(out), "message: \"default hello\"") {
		t.Errorf("rendered manifest missing default message:\n%s", out)
	}
	if !strings.Contains(string(out), "name: rel-cfg") {
		t.Errorf("rendered manifest missing release name:\n%s", out)
	}

	// Overridden values (as if edited in the ConfigMap).
	out, err = Render(chart.Chart, []byte("message: edited\n"), "rel", "ns", "v1.30.0")
	if err != nil {
		t.Fatalf("Render override: %v", err)
	}
	if !strings.Contains(string(out), `message: "edited"`) {
		t.Errorf("override not applied:\n%s", out)
	}
}
