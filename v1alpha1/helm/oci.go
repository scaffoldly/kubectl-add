package helm

import (
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/Masterminds/semver/v3"
	"helm.sh/helm/v3/pkg/registry"
)

// pullChart fetches a packaged chart from an OCI registry (oci://). The
// reference's tag comes from an explicit :tag, a ?version= query param, or —
// when neither is given — the registry's highest semver tag. Public
// registries are pulled anonymously.
func pullChart(ref *url.URL) ([]byte, error) {
	client, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("helm: creating registry client: %w", err)
	}

	// The helm reference is the registry host plus the repository path,
	// without the oci:// scheme.
	repo := ref.Host + strings.TrimSuffix(ref.Path, "/")

	target := repo
	if v := ref.Query().Get("version"); v != "" {
		target = repo + ":" + v
	} else if !hasTag(repo) {
		tag, err := latestTag(client, repo)
		if err != nil {
			return nil, err
		}
		target = repo + ":" + tag
		slog.Debug("resolved oci latest tag", "repo", repo, "tag", tag)
	}

	slog.Info("pulling oci chart", "ref", target)
	res, err := client.Pull(target, registry.PullOptWithChart(true))
	if err != nil {
		return nil, fmt.Errorf("helm: pulling %s: %w", target, err)
	}
	return res.Chart.Data, nil
}

// hasTag reports whether the repository reference already carries a :tag in
// its last path segment (guarding against a registry host's :port).
func hasTag(repo string) bool {
	last := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		last = repo[i+1:]
	}
	return strings.Contains(last, ":")
}

// latestTag returns the registry repository's highest semver tag.
func latestTag(client *registry.Client, repo string) (string, error) {
	tags, err := client.Tags(repo)
	if err != nil {
		return "", fmt.Errorf("helm: listing tags for %s: %w", repo, err)
	}
	if len(tags) == 0 {
		return "", fmt.Errorf("helm: no tags for %s", repo)
	}
	return highestSemver(tags), nil
}

// highestSemver returns the greatest semver tag, or the first tag when none
// parse as semver.
func highestSemver(tags []string) string {
	var best *semver.Version
	bestRaw := ""
	for _, t := range tags {
		v, err := semver.NewVersion(t)
		if err != nil {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best, bestRaw = v, t
		}
	}
	if bestRaw == "" {
		return tags[0]
	}
	return bestRaw
}
