//go:build !noselfupdate

package selfupdate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

// The keyless cosign identity the release workflow signs with: the workflow's
// GitHub Actions OIDC token, whose subject is a URL under this repo.
const (
	certIssuer        = "https://token.actions.githubusercontent.com"
	certIdentityRegex = `^https://github\.com/scaffoldly/kubectl-add/`
)

// verifySignature checks the cosign .sigstore bundle over the downloaded
// artifact, against the Sigstore public-good trust root and the release
// workflow's OIDC identity. It returns nil only when the bundle proves the
// artifact was signed by this repo's release workflow.
func verifySignature(artifact, bundleJSON []byte) error {
	trustedRoot, err := root.FetchTrustedRoot()
	if err != nil {
		return fmt.Errorf("fetching sigstore trust root: %w", err)
	}

	verifier, err := verify.NewVerifier(trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("building verifier: %w", err)
	}

	// bundle.LoadJSONFromPath is the only JSON entrypoint; stage the bundle in
	// a temp file.
	tmp, err := os.CreateTemp("", "kubectl-add-*.sigstore")
	if err != nil {
		return fmt.Errorf("staging bundle: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bundleJSON); err != nil {
		tmp.Close()
		return fmt.Errorf("staging bundle: %w", err)
	}
	tmp.Close()

	b, err := bundle.LoadJSONFromPath(filepath.Clean(tmp.Name()))
	if err != nil {
		return fmt.Errorf("loading bundle: %w", err)
	}

	identity, err := verify.NewShortCertificateIdentity(certIssuer, "", "", certIdentityRegex)
	if err != nil {
		return fmt.Errorf("building identity policy: %w", err)
	}

	policy := verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(artifact)),
		verify.WithCertificateIdentity(identity),
	)

	if _, err := verifier.Verify(b, policy); err != nil {
		return fmt.Errorf("signature does not verify: %w", err)
	}
	return nil
}
