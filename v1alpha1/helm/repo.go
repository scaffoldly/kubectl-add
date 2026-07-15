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
// repo's index.yaml, selects a chart, picks a version, and loads that
// packaged chart. The ?chart= and ?version= query params pin the chart and
// version; otherwise the chart is inferred (the entry named after the repo's
// last path segment, else the sole entry) and the newest version is used.
func DiscoverRepo(ctx context.Context, repoURL *url.URL, fetch Fetch) (*Chart, error) {
	q := repoURL.Query()
	wantChart, wantVersion := q.Get("chart"), q.Get("version")

	index := *repoURL
	index.RawQuery = ""
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

	name, err := chooseChart(&idx, repoURL, wantChart)
	if err != nil {
		return nil, err
	}

	version, err := selectVersion(idx.Entries[name], name, wantVersion)
	if err != nil {
		return nil, err
	}
	if len(version.URLs) == 0 {
		return nil, fmt.Errorf("helm: chart %s-%s has no download URL", name, version.Version)
	}

	tgz, err := chartURL(repoURL, version.URLs[0])
	if err != nil {
		return nil, err
	}
	slog.Info("selected chart from repo", "chart", name, "version", version.Version, "url", tgz)
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

// chooseChart picks which chart to install from a repo index. An explicit
// name (from ?chart=) wins; otherwise the entry named after the repo's last
// path segment if present, otherwise the sole entry. A multi-chart repo with
// no name match is ambiguous.
func chooseChart(idx *repo.IndexFile, repoURL *url.URL, want string) (string, error) {
	if want != "" {
		if _, ok := idx.Entries[want]; ok {
			return want, nil
		}
		return "", fmt.Errorf("helm: chart %q not found in repo %s", want, repoURL)
	}

	base := path.Base(strings.TrimSuffix(repoURL.Path, "/"))
	if _, ok := idx.Entries[base]; ok {
		return base, nil
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
	return "", fmt.Errorf("helm: repo %s has multiple charts %v; select one with ?chart=", repoURL, names)
}

// selectVersion picks a chart version: the requested one (from ?version=) or,
// when unspecified, the newest (entries are sorted newest-first).
func selectVersion(versions repo.ChartVersions, name, want string) (*repo.ChartVersion, error) {
	if len(versions) == 0 {
		return nil, fmt.Errorf("helm: chart %q has no versions", name)
	}
	if want == "" {
		return versions[0], nil
	}
	for _, v := range versions {
		if v.Version == want {
			return v, nil
		}
	}
	return nil, fmt.Errorf("helm: chart %s has no version %q", name, want)
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
	base.RawQuery = ""
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
