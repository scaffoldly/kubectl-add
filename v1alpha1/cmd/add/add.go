package add

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/git"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/http"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/image"
	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

// DefaultNamespace is used when neither -n nor the kubeconfig context set one.
const DefaultNamespace = "default"

type Add struct {
	// Resource is what to add to the cluster: a URL to a YAML manifest,
	// an oci:// chart reference, or a git repo like
	// "kubernetes/ingress-nginx".
	Resource string
	// Format is the resolved artifact format (helm, kustomize, yaml),
	// set by URL.
	Format string
	// Namespace scopes the apply.
	Namespace string
	// ConfigFlags supplies kubectl's standard connection flags.
	ConfigFlags *genericclioptions.ConfigFlags
	// RESTConfig is the cluster connection config.
	RESTConfig *rest.Config
	// Registry resolves Resource into an installable artifact.
	Registry *resolve.Registry

	// err carries the first builder failure to Run.
	err error
}

func New() *Add {
	return &Add{
		Namespace: DefaultNamespace,
		Registry: resolve.New().
			WithResolver(git.New()).
			WithResolver(image.New()).
			WithResolver(http.New()),
	}
}

// IntoCobra builds the root command: kubectl add <resource>.
func (a *Add) IntoCobra() *cobra.Command {
	return &cobra.Command{
		Use:   "kubectl-add <resource>",
		Short: "Add a resource to the cluster",
		Long:  "Add a resource to the cluster. Currently accepts a URL to a YAML manifest, which is applied with kubectl apply.",
		Annotations: map[string]string{
			cobra.CommandDisplayNameAnnotation: "kubectl add",
		},
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.WithCobra(cmd, args).Run()
		},
	}
}

// WithCobra applies parsed CLI state: the positional resource, plus the REST
// config and namespace resolved from ConfigFlags. Runs at execute time, so
// flag values are populated.
func (a *Add) WithCobra(cmd *cobra.Command, args []string) *Add {
	a.WithResource(args[0])

	if a.ConfigFlags != nil {
		config, err := a.ConfigFlags.ToRESTConfig()
		if err != nil {
			a.err = fmt.Errorf("building REST config: %w", err)
			return a
		}
		a.WithRESTConfig(config)

		namespace, _, err := a.ConfigFlags.ToRawKubeConfigLoader().Namespace()
		if err != nil {
			a.err = fmt.Errorf("resolving namespace: %w", err)
			return a
		}
		a.WithNamespace(namespace)
	}

	return a
}

func (a *Add) WithResource(resource string) *Add {
	a.Resource = resource
	return a
}

// WithNamespace sets the namespace, falling back to DefaultNamespace when empty.
func (a *Add) WithNamespace(namespace string) *Add {
	if namespace == "" {
		namespace = DefaultNamespace
	}
	a.Namespace = namespace
	return a
}

func (a *Add) WithConfigFlags(flags *genericclioptions.ConfigFlags) *Add {
	a.ConfigFlags = flags
	return a
}

func (a *Add) WithRESTConfig(config *rest.Config) *Add {
	a.RESTConfig = config
	return a
}

func (a *Add) WithRegistry(registry *resolve.Registry) *Add {
	a.Registry = registry
	return a
}

// URL distills Resource through the resolver registry into the URL to
// apply, recording the resolved Format on the way. Returns nil on failure
// and records the cause in a.err for Run to surface.
func (a *Add) URL() *url.URL {
	resolution, err := a.Registry.Resolve(a.Resource)
	if err != nil {
		a.err = err
		return nil
	}
	a.Format = resolution.Format
	return resolution.URL
}

func (a *Add) Run() error {
	if a.err != nil {
		return a.err
	}

	manifest := a.URL()
	if a.err != nil {
		return a.err
	}

	if a.Format != "yaml" {
		// TODO: install helm charts and kustomizations
		return fmt.Errorf("resolved %s to %s (%s): installing %s is not implemented yet", a.Resource, manifest, a.Format, a.Format)
	}

	args := []string{"apply", "-f", manifest.String(), "--namespace", a.Namespace}
	if a.ConfigFlags != nil {
		if context := *a.ConfigFlags.Context; context != "" {
			args = append(args, "--context", context)
		}
		if kubeconfig := *a.ConfigFlags.KubeConfig; kubeconfig != "" {
			args = append(args, "--kubeconfig", kubeconfig)
		}
	}

	fmt.Printf("applying %s\n", manifest)
	apply := exec.Command("kubectl", args...)
	apply.Stdout = os.Stdout
	apply.Stderr = os.Stderr
	return apply.Run()
}
