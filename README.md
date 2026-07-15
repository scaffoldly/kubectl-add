# kubectl-add

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

<!-- TODO(krew): once merged into the krew-index, document install via Krew:
     kubectl krew install add — see https://github.com/scaffoldly/kubectl-add/issues/2 -->

Grab a binary from [GitHub Releases](https://github.com/scaffoldly/kubectl-add/releases).

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
kubectl add https://scaffoldly.github.io/kubectl-add/kustomization/kustomization.yaml
```

### Helm

A chart served as loose files, a chart repository (its `index.yaml` is
sniffed automatically), or a GitHub repo (defaults to the latest release and
finds the chart):

```sh
# loose Chart.yaml files
kubectl add https://scaffoldly.github.io/kubectl-add/helm/Chart.yaml

# a chart repository
kubectl add https://metallb.github.io/metallb

# pin the chart and version from a repository
kubectl add "https://metallb.github.io/metallb?chart=metallb&version=0.16.1"

# a GitHub repo
kubectl add kubernetes/ingress-nginx
```

Stage a chart's values for editing before installing with `--prepare`:

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

This is open source software licensed under the [MIT License](LICENSE).
Contributions are welcome — please open an issue or pull request.
