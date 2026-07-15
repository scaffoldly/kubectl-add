package add

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/lmittmann/tint"
	"github.com/scaffoldly/kubectl-add/v1alpha1/remote"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/git"
	resolvehttp "github.com/scaffoldly/kubectl-add/v1alpha1/resolve/http"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/image"
	"github.com/spf13/cobra"
	"golang.org/x/term"
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
	// Format is the resolved artifact format, set by URL.
	Format resolve.Format
	// Namespace scopes the apply.
	Namespace string
	// Debug enables debug logging and kubectl -v=4.
	Debug bool
	// Verbose enables kubectl -v=2. Debug wins when both are set.
	Verbose bool
	// Remove deletes the resolved resource instead of adding it.
	Remove bool
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
			WithResolver(resolvehttp.New()),
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
	debug, _ := cmd.Flags().GetBool("debug")
	a.WithDebug(debug)
	verbose, _ := cmd.Flags().GetBool("verbose")
	a.WithVerbose(verbose)
	remove, _ := cmd.Flags().GetBool("remove")
	a.WithRemove(remove)
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

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
)

// logHandler prints INFO records without the time= and level= prefix,
// keeping the full structured text format for every other level.
type logHandler struct {
	slog.Handler
	out   io.Writer
	color bool
}

func (h *logHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level == slog.LevelInfo {
		line := record.Message
		if h.color {
			line = ansiBold + record.Message + ansiReset
		}
		record.Attrs(func(attr slog.Attr) bool {
			if h.color {
				line += " " + ansiDim + attr.Key + "=" + ansiReset + ansiCyan + attr.Value.String() + ansiReset
			} else {
				line += " " + attr.String()
			}
			return true
		})
		_, err := fmt.Fprintln(h.out, line)
		return err
	}
	return h.Handler.Handle(ctx, record)
}

// WithDebug configures the process-wide log level: debug logs are emitted
// to stderr only when enabled. Output is colored when stderr is a terminal
// and NO_COLOR is unset.
func (a *Add) WithDebug(debug bool) *Add {
	a.Debug = debug
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	color := term.IsTerminal(int(os.Stderr.Fd())) && os.Getenv("NO_COLOR") == ""
	slog.SetDefault(slog.New(&logHandler{
		Handler: tint.NewTextHandler(os.Stderr, &tint.Options{Level: level, NoColor: !color}),
		out:     os.Stderr,
		color:   color,
	}))
	return a
}

// WithVerbose enables verbose kubectl output.
func (a *Add) WithVerbose(verbose bool) *Add {
	a.Verbose = verbose
	return a
}

// WithRemove selects delete instead of apply.
func (a *Add) WithRemove(remove bool) *Add {
	a.Remove = remove
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
	slog.Info("resolved", "resolver", resolution.Resolver, "format", resolution.Format, "url", resolution.URL)
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

	if a.RESTConfig == nil {
		return fmt.Errorf("no REST config: provide WithConfigFlags")
	}

	ctx := context.Background()

	slog.Info("fetching", "url", manifest)
	body, err := a.fetch(ctx, manifest)
	if err != nil {
		return err
	}

	switch a.Format {
	case resolve.FormatYAML, resolve.FormatKustomize:
		// yaml is applied as-is; a kustomization is built server-side.
	default:
		// TODO: render helm charts to yaml, then apply
		return fmt.Errorf("resolved %s to %s (%s): installing %s is not implemented yet", a.Resource, manifest, a.Format, a.Format)
	}

	verbosity := 0
	if a.Verbose {
		verbosity = 2
	}
	if a.Debug {
		verbosity = 4
	}

	action := "applying"
	if a.Remove {
		action = "removing"
	}
	slog.Info(action, "url", manifest)
	return remote.New().
		WithRESTConfig(a.RESTConfig).
		WithNamespace(a.Namespace).
		WithManifest(body).
		WithFormat(a.Format).
		WithVerbosity(verbosity).
		WithRemove(a.Remove).
		WithConfigFlags(a.ConfigFlags).
		WithStreams(os.Stdout, os.Stderr).
		Run(ctx)
}

// maxRedirects bounds the redirect chain when fetching a manifest.
const maxRedirects = 10

// fetch downloads the manifest at u, following redirects (e.g. k8s.io
// short links to raw content) up to maxRedirects hops.
func (a *Add) fetch(ctx context.Context, u *url.URL) ([]byte, error) {
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			slog.Debug("following redirect", "from", via[len(via)-1].URL, "to", req.URL, "hop", len(via))
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after %d redirects", maxRedirects)
			}
			return nil
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", u, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", u, err)
	}
	return body, nil
}
