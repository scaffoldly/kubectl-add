package helm

import (
	"net/url"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Resolution builds a helm Resolution for a chart located by a transport.
func Resolution(resolver string, u *url.URL) *resolve.Resolution {
	return &resolve.Resolution{
		Resolver: resolver,
		Format:   resolve.FormatHelm,
		URL:      u,
	}
}
