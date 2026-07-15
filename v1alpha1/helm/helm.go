// Package helm discovers a chart served as loose files over HTTP and renders
// it to plain YAML with the helm SDK, client-side. HTTP can't list a
// directory, so templates are discovered by probing a set of conventional
// paths relative to the Chart.yaml URL.
package helm

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// Fetch retrieves the content at u, reporting whether it exists.
type Fetch func(ctx context.Context, u *url.URL) (content []byte, found bool, err error)

// conventionalPaths are chart files probed relative to Chart.yaml. HTTP has
// no directory listing, so charts using non-conventional template names are
// not fully discovered.
var conventionalPaths = []string{
	"values.yaml",
	"values.schema.json",
	"templates/_helpers.tpl",
	"templates/NOTES.txt",
	"templates/serviceaccount.yaml",
	"templates/configmap.yaml",
	"templates/secret.yaml",
	"templates/deployment.yaml",
	"templates/statefulset.yaml",
	"templates/daemonset.yaml",
	"templates/service.yaml",
	"templates/ingress.yaml",
	"templates/hpa.yaml",
	"templates/pvc.yaml",
	"templates/job.yaml",
	"templates/cronjob.yaml",
	"templates/role.yaml",
	"templates/rolebinding.yaml",
	"templates/clusterrole.yaml",
	"templates/clusterrolebinding.yaml",
}

// Chart is a discovered chart plus its default values (the chart's own
// values.yaml), used when no persisted values override them.
type Chart struct {
	Chart         *chart.Chart
	DefaultValues []byte
}

// Discover fetches Chart.yaml and the conventional chart files relative to
// chartURL and loads them into a chart.
func Discover(ctx context.Context, chartURL *url.URL, fetch Fetch) (*Chart, error) {
	chartYAML, found, err := fetch(ctx, chartURL)
	if err != nil {
		return nil, fmt.Errorf("helm: fetching Chart.yaml: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("helm: no Chart.yaml at %s", chartURL)
	}

	files := []*loader.BufferedFile{{Name: "Chart.yaml", Data: chartYAML}}
	var defaultValues []byte

	for _, rel := range conventionalPaths {
		u := chartURL.ResolveReference(&url.URL{Path: rel})
		content, found, err := fetch(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("helm: fetching %s: %w", rel, err)
		}
		if !found {
			continue
		}
		slog.Debug("discovered chart file", "path", rel)
		files = append(files, &loader.BufferedFile{Name: rel, Data: content})
		if rel == "values.yaml" {
			defaultValues = content
		}
	}

	ch, err := loader.LoadFiles(files)
	if err != nil {
		return nil, fmt.Errorf("helm: loading chart: %w", err)
	}
	return &Chart{Chart: ch, DefaultValues: defaultValues}, nil
}

// Render templates the chart with the given values into a multi-document
// YAML manifest, client-side (no cluster access).
func Render(ch *chart.Chart, values []byte, release, namespace string) ([]byte, error) {
	vals, err := chartutil.ReadValues(values)
	if err != nil {
		return nil, fmt.Errorf("helm: parsing values: %w", err)
	}

	cfg := &action.Configuration{Releases: storage.Init(driver.NewMemory())}
	inst := action.NewInstall(cfg)
	inst.DryRun = true
	inst.ClientOnly = true
	inst.ReleaseName = release
	inst.Namespace = namespace

	rel, err := inst.Run(ch, vals.AsMap())
	if err != nil {
		return nil, fmt.Errorf("helm: rendering chart: %w", err)
	}
	return []byte(rel.Manifest), nil
}

// ReleaseName derives a release name from the chart metadata.
func ReleaseName(ch *chart.Chart) string {
	return path.Base(ch.Name())
}
