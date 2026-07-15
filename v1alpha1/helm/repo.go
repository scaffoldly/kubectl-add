package helm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strings"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

// DiscoverRepo resolves a chart from a helm chart repository. It fetches the
// repo's index.yaml, selects a chart (preferring one named after the repo's
// last path segment, else the sole entry), takes its latest version, and
// loads that packaged chart.
func DiscoverRepo(ctx context.Context, repoURL *url.URL, fetch Fetch) (*Chart, error) {
	index := *repoURL
	index.Path = strings.TrimSuffix(repoURL.Path, "/") + "/index.yaml"

	data, found, err := fetch(ctx, &index)
	if err != nil {
		return nil, fmt.Errorf("helm: fetching repo index: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("helm: no index.yaml at %s", &index)
	}

	var idx repo.IndexFile
	if err := yaml.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("helm: parsing repo index %s: %w", &index, err)
	}
	idx.SortEntries()

	name, err := chooseChart(&idx, repoURL)
	if err != nil {
		return nil, err
	}

	versions := idx.Entries[name]
	if len(versions) == 0 {
		return nil, fmt.Errorf("helm: chart %q has no versions in %s", name, &index)
	}
	// SortEntries orders each chart's versions newest-first.
	latest := versions[0]
	if len(latest.URLs) == 0 {
		return nil, fmt.Errorf("helm: chart %s-%s has no download URL", name, latest.Version)
	}

	tgz, err := chartURL(repoURL, latest.URLs[0])
	if err != nil {
		return nil, err
	}
	slog.Info("selected chart from repo", "chart", name, "version", latest.Version, "url", tgz)
	return DiscoverArchive(ctx, tgz, fetch)
}

// DiscoverArchive loads a packaged chart (.tgz) fetched from u.
func DiscoverArchive(ctx context.Context, u *url.URL, fetch Fetch) (*Chart, error) {
	data, found, err := fetch(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("helm: fetching chart archive: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("helm: no chart archive at %s", u)
	}

	ch, err := loader.LoadArchive(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("helm: loading chart archive %s: %w", u, err)
	}
	return &Chart{Chart: ch, DefaultValues: rawValues(ch)}, nil
}

// chooseChart picks which chart to install from a repo index: the one named
// after the repo's last path segment if present, otherwise the sole entry.
// A multi-chart repo with no name match is ambiguous.
func chooseChart(idx *repo.IndexFile, repoURL *url.URL) (string, error) {
	want := path.Base(strings.TrimSuffix(repoURL.Path, "/"))
	if _, ok := idx.Entries[want]; ok {
		return want, nil
	}
	if len(idx.Entries) == 1 {
		for name := range idx.Entries {
			return name, nil
		}
	}

	names := make([]string, 0, len(idx.Entries))
	for name := range idx.Entries {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("helm: repo %s has multiple charts %v; reference one by name", repoURL, names)
}

// chartURL resolves an index entry's download URL, which may be absolute or
// relative to the repository.
func chartURL(repoURL *url.URL, ref string) (*url.URL, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("helm: parsing chart URL %q: %w", ref, err)
	}
	if u.IsAbs() {
		return u, nil
	}
	// Relative URLs are relative to the repo root (with a trailing slash so
	// the last path segment is treated as a directory).
	base := *repoURL
	base.Path = strings.TrimSuffix(repoURL.Path, "/") + "/"
	return base.ResolveReference(u), nil
}

// rawValues returns the chart's own values.yaml, the defaults used when no
// values are persisted.
func rawValues(ch *chart.Chart) []byte {
	for _, f := range ch.Raw {
		if f.Name == "values.yaml" {
			return f.Data
		}
	}
	return nil
}
