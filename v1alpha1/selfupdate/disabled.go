//go:build noselfupdate

// Package selfupdate is compiled out by the noselfupdate build tag: builds for
// managed channels that forbid self-updating software (homebrew-core, nixpkgs,
// future deb/rpm) ship this stub instead, so the sigstore/update machinery is
// not linked in at all.
package selfupdate

import (
	"context"
	"fmt"
	"net/http"
)

// AutoUpdate is a no-op in noselfupdate builds.
func AutoUpdate(ctx context.Context, current, token string, client *http.Client) {}

// Update reports that self-update is unavailable in this build.
func Update(ctx context.Context, current, token string, client *http.Client) error {
	return fmt.Errorf("self-update is disabled in this build; update via your package manager")
}
