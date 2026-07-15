package kustomize

import (
	"net/url"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Format names the kustomize artifact format.
const Format = "kustomize"

// Resolution builds a kustomize Resolution for a kustomization located by
// a transport.
func Resolution(resolver string, u *url.URL) *resolve.Resolution {
	return &resolve.Resolution{
		Resolver: resolver,
		Format:   Format,
		URL:      u,
	}
}

// TODO: install logic (kubectl apply -k against the located kustomization)
