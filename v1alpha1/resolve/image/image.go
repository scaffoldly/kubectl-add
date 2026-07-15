package image

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/internal/helm"
)

// Resolver is the OCI image transport: it recognizes resources that live
// in a container registry (oci://... and bare registry references). Charts
// are the only OCI artifact kubectl-add installs, so everything resolves
// to the helm format.
type Resolver struct{}

func New() *Resolver {
	return &Resolver{}
}

func (r *Resolver) Name() string {
	return "image"
}

// Detect reports whether the resource is an OCI registry reference.
func (r *Resolver) Detect(resource string) bool {
	if strings.HasPrefix(resource, "oci://") {
		return true
	}
	// TODO: sniff bare registry refs (host[:port]/path[:tag|@digest])
	return false
}

// Resolve normalizes the OCI reference into a helm Resolution.
func (r *Resolver) Resolve(resource string) (*resolve.Resolution, error) {
	u, err := url.Parse(resource)
	if err != nil {
		return nil, fmt.Errorf("image resolver: parsing %q: %w", resource, err)
	}
	// TODO: Reference — resolve the tag (latest release) when none is given
	return helm.Resolution(r.Name(), u), nil
}

// Reference normalizes the resource into a pullable registry reference,
// resolving the tag (latest release) when none is given.
func (r *Resolver) Reference(resource string) (string, error) {
	// TODO: parse and normalize the OCI reference
	return "", fmt.Errorf("image resolver: Reference not implemented")
}
