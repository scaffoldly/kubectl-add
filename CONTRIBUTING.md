# Contributing

This document is for everyone working on `kubectl-add` — humans and AI agents
alike. It covers the layout, the local dev loop, the conventions that bite, and
how a change gets from an issue to a release.

## Where to find things

Deep-link by filename; line numbers will drift.

| Topic                                    | Source                                                                    |
| ---------------------------------------- | ------------------------------------------------------------------------- |
| CLI entrypoint (`kubectl add`)           | [`cmd/kubectl-add/main.go`](./cmd/kubectl-add/main.go)                     |
| Library façade (`New`, type aliases)     | [`lib.go`](./lib.go)                                                       |
| Command builder + run loop               | [`v1alpha1/cmd/add/add.go`](./v1alpha1/cmd/add/add.go)                     |
| Resolver registry + `Resolver` interface | [`v1alpha1/resolve/resolve.go`](./v1alpha1/resolve/resolve.go)            |
| Git transport (github/gitlab/bitbucket)  | [`v1alpha1/resolve/git/`](./v1alpha1/resolve/git)                        |
| HTTP + OCI transports                    | [`v1alpha1/resolve/http/`](./v1alpha1/resolve/http), [`image/`](./v1alpha1/resolve/image) |
| Server-side apply (forward credential)   | [`v1alpha1/remote/`](./v1alpha1/remote)                                   |
| Helm render / repo / OCI pull            | [`v1alpha1/helm/`](./v1alpha1/helm)                                       |
| Kustomize materialization                | [`v1alpha1/kustomize/`](./v1alpha1/kustomize)                            |
| Shared HTTP client + build version       | [`v1alpha1/httpclient/`](./v1alpha1/httpclient), [`version/`](./v1alpha1/version) |
| e2e harness + runner                     | [`test/e2e/e2e_test.go`](./test/e2e/e2e_test.go)                          |
| Build / lint / test commands             | [`Makefile`](./Makefile)                                                  |
| CI matrix                                | [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)                  |
| Release (build + publish binaries)       | [`.github/workflows/release.yaml`](./.github/workflows/release.yaml)      |
| CodeQL scan                              | [`.github/workflows/codeql.yml`](./.github/workflows/codeql.yml)          |
| OpenSSF Scorecard scan                   | [`.github/workflows/scorecard.yml`](./.github/workflows/scorecard.yml)    |
| Dependabot config                        | [`.github/dependabot.yml`](./.github/dependabot.yml)                      |
| Security policy                          | [`SECURITY.md`](./SECURITY.md)                                            |

## Architecture

`kubectl add <resource>` runs in two stages:

1. **Resolve.** The [`resolve.Registry`](./v1alpha1/resolve/resolve.go) holds
   transports (git, image, http) in priority order. The first to `Detect` the
   resource `Resolve`s it — sniffing the content to pick an artifact format
   (yaml, kustomize, helm) and distilling it to a fetchable URL. New source
   support is a new `Resolver`; new hosts within the git transport are new
   `provider`s ([`git/github.go`](./v1alpha1/resolve/git/github.go), etc.).

2. **Apply, server-side, as you.** [`v1alpha1/remote`](./v1alpha1/remote)
   forwards the *caller's own* credential (client cert, static bearer token, or
   an exec/auth-plugin token) into a short-lived Secret and runs a throwaway
   pod that applies the manifest under the connected user's identity and RBAC.
   No ServiceAccount is created; no privileges are granted. This is a security
   boundary — see [`credential.go`](./v1alpha1/remote/credential.go) and keep
   it **fail-closed**.

Helm charts are rendered client-side with the helm SDK; kustomizations are
built in a second, credential-less pod and piped to the applier.

## Module layout

```
github.com/scaffoldly/kubectl-add           — library façade (New, type aliases)
github.com/scaffoldly/kubectl-add/cmd/...    — the kubectl-add binary
github.com/scaffoldly/kubectl-add/v1alpha1/… — implementation; may change
```

Library consumers import the root; the CLI lives under `cmd/`. Implementation
packages under `v1alpha1/` are alpha and may change between revisions.

## Local development

Requires Go (the floor is the `go` directive in [`go.mod`](./go.mod)).

```sh
git clone https://github.com/scaffoldly/kubectl-add.git
cd kubectl-add
make vet
make build      # -> ./kubectl-add
make test       # unit tests (fast; -short skips network)
make install    # -> $HOME/.local/bin/kubectl-add, for `kubectl add`
```

## Test layout

- **`*_test.go` next to the code** — unit tests with fabricated inputs or
  fakes. Some carry live-network cases guarded by `testing.Short()`; `go test
  -short` skips them.
- **`test/e2e/`** — the [`e2e_test.go`](./test/e2e/e2e_test.go) harness applies
  real resources against a live cluster and asserts on the result. It needs a
  reachable cluster and is **not** part of the cross-OS/arch CI matrix; run it
  with `make test-e2e`.

## Before you push

- `gofmt -w .`
- `make vet`
- `make test`

CI runs the same across a linux/windows/macos × x64/arm64 matrix on every PR.

## Conventions that bite

- **The apply must run as the caller.** Anything in `v1alpha1/remote` that
  could broaden the identity (fall back to a ServiceAccount, cache a token,
  leave the Secret behind) is a security regression. When the credential can't
  be determined, fail closed.
- **Actions are pinned to commit SHAs** with a version comment
  (`uses: actions/checkout@<sha> # v7.0.0`). Dependabot bumps them; keep the
  convention when adding steps. Pin cosign/attestation actions to the commit
  under the tag, not the annotated-tag object — Sigstore verification rejects
  tag-object SHAs ("imposter commit").
- **`main`'s required checks are the matrix cell names**, not a bare `ci`
  (e.g. `ci (ubuntu-24.04, stable)`). Renaming a matrix axis — bumping an image
  label, adding/removing a `go` value — renames the check context, so update
  the branch-protection required contexts in the same change or merges will
  block on a context that no longer reports.
- **`make build` injects the version** via `-ldflags -X …/version.Version`.
  Plain `go build` leaves the `dev` default (with a build-info fallback) — fine
  for local work, but release binaries must carry the tag.
- **Keep the README in sync.** The README's compatibility matrix and examples
  mirror what the resolvers actually support; a new source/format or a new
  example updates the README in the same PR.

## Branch / PR flow

**Every change starts with an issue.** The PR body carries a `Closes #<n>` line
so the merge auto-closes the tracking issue.

```sh
gh issue create --title "…" --body "…"                    # 1. issue first
git switch -c <type>/<topic>                              # 2. branch
# ... edits, commit ...
git push -u origin <type>/<topic>
gh pr create --title "<type>: …" --body "Closes #<n>. …"  # 3. PR refs the issue
gh pr merge <pr#> --squash --delete-branch                # 4. after CI is green
```

Don't commit secrets. Signed commits are preferred; CI does not enforce them.

## Commit messages

Short subject (≤ 72 chars), imperative mood ("Add X", not "Added X"). Wrap the
body at ~72 cols. Explain the *why*; the diff covers the *what*.

## Releasing

Releases ship prebuilt binaries. The
[`release.yaml`](./.github/workflows/release.yaml) workflow builds the
OS × arch matrix and publishes a GitHub Release; tag pushes (`vMAJOR.MINOR.PATCH`,
Go module semver) are the version of record. See the workflow for the current
trigger and draft-release flow.

## License

By contributing you agree your contributions are licensed under the
[MIT License](./LICENSE).
