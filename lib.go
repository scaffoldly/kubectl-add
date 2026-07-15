// Package kubectladd installs resources into a Kubernetes cluster from a
// single reference — a YAML URL, a kustomization, a helm chart, a chart
// repository, or a GitHub repo — resolving the format and applying it
// server-side as the connected user.
//
// It re-exports the fluent builder from v1alpha1/cmd/add so consumers can
// depend on the module root:
//
//	kubectladd.New().
//		WithResource("https://github.com/some/repo").
//		WithNamespace("my-namespace").
//		Run()
package kubectladd

import (
	"github.com/scaffoldly/kubectl-add/v1alpha1/cmd/add"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Add is the fluent builder for installing a resource into a cluster. See its
// With* methods for the configurable surface (WithResource, WithNamespace,
// WithConfigFlags, WithRESTConfig, WithRegistry, WithRemove, WithNoEdit,
// WithDebug, WithVerbose) and Run to execute.
type Add = add.Add

// Registry is the pluggable resolver registry that maps a reference to an
// installable artifact; pass a custom one with Add.WithRegistry.
type Registry = resolve.Registry

// New returns a new Add builder.
func New() *Add { return add.New() }
