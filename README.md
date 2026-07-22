# kubectl-add

[![Go Reference](https://pkg.go.dev/badge/github.com/scaffoldly/kubectl-add.svg)](https://pkg.go.dev/github.com/scaffoldly/kubectl-add)
[![CI](https://github.com/scaffoldly/kubectl-add/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/scaffoldly/kubectl-add/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/scaffoldly/kubectl-add/badge)](https://scorecard.dev/viewer/?uri=github.com/scaffoldly/kubectl-add)
[![Release](https://img.shields.io/github/v/release/scaffoldly/kubectl-add)](https://github.com/scaffoldly/kubectl-add/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

`kubectl add` is a kubectl plugin that installs things into your cluster from
whatever you point it at — a YAML URL, a kustomization, a helm chart, a helm
chart repository, or a git repo — and applies them **server-side**, so it
feels like native kubectl.

You give it a resource; the plugin sniffs out what it is, resolves the
installable artifact, and applies it inside the cluster **as you** — forwarding
your own credential into a short-lived Secret, so the apply is constrained by
your RBAC and no kubeconfig is copied in.

<!-- TODO: screencast GIF -->
<!-- TODO: resolution/server-side-apply diagram -->

## Features

- 🧠 **Smart resolution** — point it at a YAML URL, a kustomization, a helm chart, a chart repo, or a git repo (GitHub, GitLab, Bitbucket); it sniffs out what it is.
- ☁️ **Server-side apply** — runs `kubectl` inside the cluster; your local kubeconfig is never copied in.
- 🔑 **Runs as you** — forwards your own credential into a short-lived Secret; the apply uses your identity and RBAC, no ServiceAccount, no escalation.
- 🎛️ **Kustomize, built in-cluster** — relative resources, `bases`, nested kustomizations, and remote git/http references all resolve.
- ⎈ **Helm, no tiller, no fuss** — renders charts client-side; installs from loose files, an HTTP chart repository, or a git repo.
- 📌 **Pin what you want** — `?chart=` and `?version=` select straight from a repository index.
- 🗑️ **Reversible** — `--remove` deletes exactly what you added.
- 🌐 **Public tunnel** — `kubectl add tunnel` exposes the API server (or a service) over a Cloudflare quick tunnel, raw-forwarded so callers still authenticate.
- 🪶 **Feels native** — one command, standard kubectl flags (`--namespace`, `-v`, kubeconfig).

## Getting Started

### Setup

<!-- The krew manifest (.krew.yaml) is ready and release-automated via
     krew-release-bot; `kubectl krew install add` lands once the first
     krew-index submission is accepted. See #2. -->

**Homebrew** (macOS and Linux):

```sh
brew tap scaffoldly/tap
brew install kubectl-add
```

Or grab a binary from
[GitHub Releases](https://github.com/scaffoldly/kubectl-add/releases) — each
archive holds the `kubectl-add` binary plus the LICENSE, and every release is
cosign-signed and SLSA-attested (verification in [SECURITY.md](./SECURITY.md)).
Put `kubectl-add` on your `PATH` and kubectl discovers it as `kubectl add`.

Or build from source:

```sh
git clone https://github.com/scaffoldly/kubectl-add
cd kubectl-add
make install   # builds to $HOME/.local/bin/kubectl-add
```

Confirm kubectl sees it:

```sh
kubectl add --help
```

### Staying current

A binary installed from the GitHub release, `make install`, the curl script, or
the **Homebrew tap** keeps itself up to date: on a normal run it checks for a
newer release at most once a day and, if there is one, downloads it,
**verifies the checksum and cosign signature**, and swaps it in — repointing
the symlink for the versioned layout, or replacing the binary in place for a
tap/bare install. Updates fail open: a hiccup never blocks your `kubectl add`.

```sh
kubectl add --update                  # update now, then exit
export KUBECTL_ADD_NO_AUTO_UPDATE=1   # disable the automatic daily check
```

Installs owned by a manager with a read-only or self-managed store — **krew**
and **Nix** — are left alone; the updater points you at
`kubectl krew upgrade` / `nix profile upgrade` instead. (A future
homebrew-core build will opt out at compile time, since core forbids
self-updating software; the tap build does not.)

Point it at a resource and it resolves the format and applies it server-side.
Scope any of these to a namespace with `--namespace`, or undo with `--remove`:

```sh
kubectl add <resource> --namespace demo
kubectl add <resource> --remove
```

### YAML

A URL to one or more manifests, applied as-is:

```sh
kubectl add https://k8s.io/examples/application/deployment.yaml
```

### Kustomize

A kustomization, built server-side (relative resources, bases, and remote
git/http references are all resolved):

```sh
kubectl add https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/examples/helloWorld/kustomization.yaml
```

### Helm

A chart repository (its `index.yaml` is sniffed automatically), an OCI
registry, a git repo (defaults to the latest release and finds the chart),
or a chart served as loose `Chart.yaml` files over HTTP:

```sh
# a chart repository (picks the chart, latest version)
kubectl add https://kubernetes.github.io/ingress-nginx

# pin the chart and version from a repository
kubectl add "https://kubernetes.github.io/ingress-nginx?chart=ingress-nginx&version=4.15.1"

# an OCI registry (latest tag, or pin with :tag or ?version=)
kubectl add oci://mirror.gcr.io/bitnamicharts/nginx

# a GitHub repo
kubectl add kubernetes/ingress-nginx
```

Installing a chart opens its values in your editor first (the reconciled
ConfigMap); save to continue, or skip the edit with `--no-edit`:

```sh
kubectl add https://kubernetes.github.io/ingress-nginx            # edits, then installs
kubectl add https://kubernetes.github.io/ingress-nginx --no-edit  # installs with saved values
```

The edit is skipped automatically when stdin is not a terminal (scripts, CI).

### Tunnel

Expose the API server — or a Service reached through it — to the public
internet over a [Cloudflare quick tunnel](https://github.com/cnuss/libtunnel),
driven in-process (no `cloudflared` binary). The public URL is printed and the
tunnel runs until you interrupt it:

```sh
kubectl add tunnel                       # the API server (default)
kubectl add tunnel kubernetes            # the API server (explicit)
kubectl add tunnel svc/my-app -n demo    # a Service, via the API server proxy
```

The tunnel forwards **raw**: it re-originates TLS to the API server trusting
only the cluster CA and injects **no credentials of its own**. Callers hitting
the public URL authenticate exactly as they would against the API server
directly — a Service target routes through the `services/proxy` subresource, so
the caller needs the matching RBAC. Nothing is granted by opening the tunnel.

> ⚠️ This publishes an API server endpoint to the internet for the tunnel's
> lifetime. Anyone with the URL can reach it (and still has to authenticate).
> Use it for short-lived demos and debugging, not as a standing ingress.

## Use as a library

The same fluent builder is importable from the module root:

```go
import "github.com/scaffoldly/kubectl-add"

err := kubectladd.New().
    WithResource("https://github.com/some/repo").
    WithNamespace("my-namespace").
    Run()
```

The connection is inferred from your kubeconfig, or supply one explicitly with
`WithRESTConfig(cfg)`. Pass a `context.Context` for cancellation/deadlines with
`WithContext(ctx)`.

## Compatibility

What `kubectl add` can resolve and install, by source and format:

| Source                                                    | YAML             | Kustomize                 | Helm                                           |
| --------------------------------------------------------- | ---------------- | ------------------------- | ---------------------------------------------- |
| HTTP(S) URL                                               | ✅               | ✅                        | ✅ &nbsp;loose `Chart.yaml`, repo `index.yaml` |
| GitHub &nbsp;(`org/repo`, `.git`, `…/tree/…`, `…/blob/…`) | ✅ &nbsp;`blob/` | ✅ &nbsp;root or a subdir | ✅ &nbsp;`charts/` or a subdir, full file set  |
| GitLab &nbsp;(`…/-/tree/…`, `…/-/blob/…`, subgroups)      | ✅ &nbsp;`blob/` | ✅ &nbsp;root or a subdir | ✅ &nbsp;`charts/` or a subdir, full file set  |
| Bitbucket &nbsp;(`…/src/…`)                               | ✅               | ✅ &nbsp;root or a subdir | ✅ &nbsp;`charts/` or a subdir, full file set  |
| OCI &nbsp;(`oci://`)                                      | —                | —                         | ✅ &nbsp;registry, latest tag or pinned        |

✅ supported &nbsp;·&nbsp; 🚧 planned &nbsp;·&nbsp; — n/a

Notes:

- A bare `org/repo` resolves at the repo's latest release (GitHub default host);
  a full URL pins the ref and subpath — GitHub/GitLab via `…/tree/<ref>/<path>`
  or `…/blob/<ref>/<file>` (GitLab prefixes these with `/-/`), Bitbucket via
  `…/src/<ref>/<path>`. Each resolves to its host's raw URL, and helm charts
  fetch their full file set via the git tree (no conventional-path guessing).
  Bitbucket has no releases, so a bare repo resolves at its latest tag (then the
  main branch). Set `GITHUB_TOKEN` / `GITLAB_TOKEN` / `BITBUCKET_TOKEN` to raise
  rate limits or reach private repos.
- Kustomizations sourced from a URL support relative resources, `bases`, nested
  kustomizations, and remote git/http references. Some kustomize fields that
  reference local files are not yet materialized ([#1]).
- Helm charts install from loose files, an HTTP chart repository, or an OCI
  registry (including repositories whose `index.yaml` points at `oci://`).
  `?chart=` and `?version=` pin the selection.

[#1]: https://github.com/scaffoldly/kubectl-add/issues/1

## How it works

`kubectl add` never runs `kubectl apply` locally. Instead it:

1. **Resolves** the resource through pluggable transports (git, http, image),
   each sniffing the content to pick a format (yaml, kustomize, helm).
2. **Forwards your credential** — the client certificate or bearer token from
   your kubeconfig — into a short-lived Secret, alongside the cluster CA.
3. **Runs** a throwaway pod (`bitnami/kubectl`) that applies the manifest
   file-less — `--server`, `--certificate-authority`, and your
   `--token`/`--client-certificate` — with the manifest streamed to its stdin
   (never persisted to etcd).

The apply runs **as you**: it's attributed to your identity and constrained by
your own RBAC. No ServiceAccount is created and no privileges are granted.

Kustomizations are built in a second, credential-less pod and piped to the
applier; the plugin binary is the pipe. Helm charts are rendered client-side
with the helm SDK and the rendered manifest is applied the same way.

## Troubleshooting

### Permissions

The apply runs with your own credential, so it can only do what you can. If it
fails with a forbidden error, you lack the RBAC to apply that resource — grant
it to your user, or run as one who has it. Authentication methods that can't be
forwarded (some exec/OIDC setups that return a client certificate rather than a
token) will error rather than silently escalate.

### Helm values

The values used for a chart are persisted in a ConfigMap
(`kubectl-add-values-<hash>`) in the target namespace, keyed by the chart URL.
The first install stores the chart's defaults; later installs reuse them. Each
install opens the ConfigMap in your editor before rendering (unless `--no-edit`
or a non-interactive stdin), so you can review and adjust the values.

### Verbose output

```sh
kubectl add <resource> --verbose   # -v=2 on the remote kubectl
kubectl add <resource> --debug     # -v=4, plus local debug logs
```

## Docs

- Examples: [`test/e2e`](test/e2e) in this repo
- Issues: <https://github.com/scaffoldly/kubectl-add/issues>

## Contributions

This is open source software licensed under the [MIT License](LICENSE).
Contributions are welcome — please open an issue or pull request.
