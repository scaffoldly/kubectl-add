package yaml

import (
	"net/url"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Resolution builds a yaml Resolution for a manifest located by a transport.
func Resolution(resolver string, u *url.URL) *resolve.Resolution {
	return &resolve.Resolution{
		Resolver: resolver,
		Format:   resolve.FormatYAML,
		URL:      u,
	}
}
