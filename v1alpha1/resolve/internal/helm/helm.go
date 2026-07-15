package helm

import (
	"net/url"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
)

// Format names the helm chart artifact format.
const Format = "helm"

// Resolution builds a helm Resolution for a chart located by a transport.
func Resolution(resolver string, u *url.URL) *resolve.Resolution {
	return &resolve.Resolution{
		Resolver: resolver,
		Format:   Format,
		URL:      u,
	}
}

// TODO: install logic (helm template/install against the located chart)
