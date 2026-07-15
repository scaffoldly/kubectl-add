package yaml

import (
	"net/url"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Format names the plain YAML manifest artifact format.
const Format = "yaml"

// Resolution builds a yaml Resolution for a manifest located by a transport.
func Resolution(resolver string, u *url.URL) *resolve.Resolution {
	return &resolve.Resolution{
		Resolver: resolver,
		Format:   Format,
		URL:      u,
	}
}

// TODO: install logic (kubectl apply -f against the located manifest)
