# kubectl-add

[![go](https://github.com/scaffoldly/kubectl-add/actions/workflows/pages.yml/badge.svg)](https://github.com/scaffoldly/kubectl-add/actions/workflows/pages.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/scaffoldly/kubectl-add)](https://goreportcard.com/report/github.com/scaffoldly/kubectl-add)

`kubectl add` is a kubectl plugin that installs things into your cluster from
whatever you point it at — a YAML URL, a kustomization, a helm chart, a helm
chart repository, or a GitHub repo — and applies them **server-side**, so it
feels like native kubectl.

You give it a resource; the plugin sniffs out what it is, resolves the
installable artifact, and applies it inside the cluster using a short-lived,
minted ServiceAccount token — your local kubeconfig is never copied into the
cluster.

<!-- TODO: screencast GIF -->
<!-- TODO: resolution/server-side-apply diagram -->

## Getting Started

### Setup

Install with [Krew](https://krew.sigs.k8s.io/):

```sh
kubectl krew install add
```

<!-- TODO: not yet published to the krew-index; the line above is the target. -->

Or grab a binary from [GitHub Releases](https://github.com/scaffoldly/kubectl-add/releases).

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

### Run

Point it at a manifest and it applies server-side:

```sh
kubectl add https://scaffoldly.github.io/kubectl-add/yaml/nginx.yaml
```

It resolves and installs several formats:

```sh
# a kustomization (built server-side)
kubectl add https://scaffoldly.github.io/kubectl-add/kustomization/kustomization.yaml

# a helm chart served as loose files
kubectl add https://scaffoldly.github.io/kubectl-add/helm/Chart.yaml

# a helm chart repository (index.yaml is sniffed automatically)
kubectl add https://metallb.github.io/metallb

# pin the chart and version from a repository
kubectl add "https://metallb.github.io/metallb?chart=metallb&version=0.16.1"

# a GitHub repo (defaults to the latest release, finds the chart)
kubectl add kubernetes/ingress-nginx
```

Scope it to a namespace, or remove what you added:

```sh
kubectl add https://scaffoldly.github.io/kubectl-add/yaml/nginx.yaml --namespace demo
kubectl add https://scaffoldly.github.io/kubectl-add/yaml/nginx.yaml --remove
```

For helm charts, stage the values for editing before installing:

```sh
kubectl add https://metallb.github.io/metallb --prepare
kubectl edit configmap kubectl-add-values-<hash> -n <namespace>
kubectl add https://metallb.github.io/metallb
```

## How it works

`kubectl add` never runs `kubectl apply` locally. Instead it:

1. **Resolves** the resource through pluggable transports (git, http, image),
   each sniffing the content to pick a format (yaml, kustomize, helm).
2. **Mints** a short-lived ServiceAccount token via the TokenRequest API and
   stores it, with the cluster CA, in an in-cluster Secret.
3. **Runs** a throwaway pod (`bitnami/kubectl`) that applies the manifest
   file-less — `--server`, `--certificate-authority`, `--token` — with the
   manifest streamed to its stdin (never persisted to etcd).

Kustomizations are built in a second, credential-less pod and piped to the
applier; the plugin binary is the pipe. Helm charts are rendered client-side
with the helm SDK and the rendered manifest is applied the same way.

## Troubleshooting

### Permissions

The runner ServiceAccount (`kubectl-add`) is bound to `cluster-admin` so it can
apply arbitrary manifests. The binding is created idempotently in the target
namespace and left in place between runs.

### Helm values

The values used for a chart are persisted in a ConfigMap
(`kubectl-add-values-<hash>`) in the target namespace, keyed by the chart URL.
The first install stores the chart's defaults; later installs reuse them. Use
`--prepare` to stage and `kubectl edit` them before installing.

### Verbose output

```sh
kubectl add <resource> --verbose   # -v=2 on the remote kubectl
kubectl add <resource> --debug     # -v=4, plus local debug logs
```

## Docs

- Live examples: <https://scaffoldly.github.io/kubectl-add/>
- Issues: <https://github.com/scaffoldly/kubectl-add/issues>

## Contributions

This is an open source project. Contributions are welcome — please open an
issue or pull request.
