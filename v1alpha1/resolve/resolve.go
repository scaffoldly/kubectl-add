package resolve

import (
	"fmt"
	"net/url"
)

// Resolution is the result of resolving a resource: a concrete artifact
// kubectl-add knows how to install.
type Resolution struct {
	// Resolver is the name of the transport resolver that produced this
	// resolution, e.g. "git", "http", "image".
	Resolver string
	// Format is the artifact format, e.g. "helm", "kustomize", "yaml".
	Format string
	// URL locates the resolved artifact (manifest, chart, kustomization).
	URL *url.URL
}

// Resolver is a transport: it recognizes where a resource lives (git repo,
// http(s) URL, OCI registry) and sniffs its content to determine the
// artifact format. Implementations live in the subpackages of this package
// (git, http, image).
type Resolver interface {
	// Name identifies the transport, e.g. "git", "http", "image".
	Name() string
	// Detect reports whether this transport recognizes the resource, by
	// string inspection only — no I/O. When the resource carries an
	// explicit protocol (http://, https://, oci://) detection is by
	// scheme; otherwise transports sniff the shape of the string (e.g.
	// "org/repo" is a git repo on github.com).
	Detect(resource string) bool
	// Resolve fetches through the transport, sniffs the artifact format
	// (helm, kustomize, yaml), and distills the resource into a
	// Resolution.
	Resolve(resource string) (*Resolution, error)
}

// Registry holds resolvers in priority order; the first resolver to detect
// a resource wins. Compose it at the wiring site — recommended order:
// git, image, then http as the fallback.
type Registry struct {
	resolvers []Resolver
}

func New() *Registry {
	return &Registry{}
}

func (r *Registry) WithResolver(resolver Resolver) *Registry {
	r.resolvers = append(r.resolvers, resolver)
	return r
}

// Resolve routes the resource to the first resolver that detects it.
func (r *Registry) Resolve(resource string) (*Resolution, error) {
	for _, resolver := range r.resolvers {
		if resolver.Detect(resource) {
			return resolver.Resolve(resource)
		}
	}
	return nil, fmt.Errorf("no resolver recognizes resource %q", resource)
}
