# Security Policy

## Supported Versions

`kubectl-add` is pre-1.0 (`0.x`). Only the latest tagged release receives
security fixes. Once `1.x` ships, the support window will move to "latest
minor" of the current major.

| Version      | Supported          |
| ------------ | ------------------ |
| latest `0.x` | :white_check_mark: |
| older `0.x`  | :x:                |

## Reporting a Vulnerability

Please report security vulnerabilities **privately** via GitHub's
[private vulnerability reporting](https://github.com/scaffoldly/kubectl-add/security/advisories/new)
on the Security tab. That opens a draft advisory only the maintainers can see.

Please do **not** open a public issue for a suspected vulnerability.

### What to include

- A clear description of the issue and its impact.
- Steps to reproduce (a minimal example, version/commit, OS).
- Whether the issue is exploitable with default configuration.
- Any suggested fix or mitigation, if you have one.

### Expectations

- Acknowledgement within 7 days.
- A status update within 30 days, including a plan and rough timeline.
- A coordinated disclosure once a fix or workaround is available; we will
  credit you in the advisory unless you ask otherwise.

## Verifying releases

Every release archive is signed and attested by the release workflow:

- **cosign** (keyless: Sigstore Fulcio cert + Rekor transparency log) — proves
  *who* published the artifact. Each archive ships a self-contained
  `<archive>.sigstore` bundle.
- **SLSA build provenance** (`actions/attest-build-provenance`) — attests
  *how and where* it was built (workflow, commit, builder). Attestations live
  in the repo's attestation store; nothing extra is attached to the release.

Verify the cosign signature (cosign v2+):

```sh
ARCHIVE=kubectl-add_linux_amd64.tar.gz   # the archive you downloaded
REPO=scaffoldly/kubectl-add
TAG=v0.1.0

gh release download "$TAG" -R "$REPO" --pattern "$ARCHIVE" --pattern "$ARCHIVE.sigstore"

cosign verify-blob \
  --bundle "$ARCHIVE.sigstore" \
  --certificate-identity-regexp '^https://github.com/scaffoldly/kubectl-add/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "$ARCHIVE"
```

Verify the build provenance:

```sh
gh attestation verify "$ARCHIVE" --repo scaffoldly/kubectl-add
```

`Verified OK` (cosign) and a matched attestation (gh) mean the archive is
exactly what the release workflow built and signed.

## Scope

`kubectl-add` resolves a resource, then applies it **server-side as the
connected user** — it forwards the caller's own cluster credential into a
short-lived Secret so the apply runs under their identity and RBAC. Because it
handles credentials and executes remote applies, the following are especially
in scope:

- Credential handling: leakage of the forwarded token/certificate, the
  short-lived Secret outliving the run, or the runner pod exposing it.
- Privilege escalation: any path where the apply runs as an identity broader
  than the connected user (a ServiceAccount, cluster-admin, etc.).
- Resolver/transport handling of untrusted input (git/http/OCI references)
  that leads to fetching or executing something the user did not point at.

Out of scope: vulnerabilities in third-party dependencies or the Go standard
library itself (report those to their respective projects), and issues that
require an attacker to already have local execution as the same user.
