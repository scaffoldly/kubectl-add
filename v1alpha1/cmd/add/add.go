package add

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strings"

	"github.com/lmittmann/tint"
	"github.com/scaffoldly/kubectl-add/v1alpha1/helm"
	"github.com/scaffoldly/kubectl-add/v1alpha1/kustomize"
	"github.com/scaffoldly/kubectl-add/v1alpha1/remote"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/git"
	resolvehttp "github.com/scaffoldly/kubectl-add/v1alpha1/resolve/http"
	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve/image"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
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
	// NoEdit skips the interactive edit of an install's editable inputs
	// before applying. The edit is also skipped when stdin is not a
	// terminal.
	NoEdit bool
	// ConfigFlags supplies kubectl's standard connection flags.
	ConfigFlags *genericclioptions.ConfigFlags
	// RESTConfig is the cluster connection config. When unset, Run infers it
	// from the kubeconfig.
	RESTConfig *rest.Config
	// Context carries cancellation/deadline into Run; defaults to
	// context.Background when unset.
	Context context.Context
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
	a.WithContext(cmd.Context())
	debug, _ := cmd.Flags().GetBool("debug")
	a.WithDebug(debug)
	verbose, _ := cmd.Flags().GetBool("verbose")
	a.WithVerbose(verbose)
	remove, _ := cmd.Flags().GetBool("remove")
	a.WithRemove(remove)
	noEdit, _ := cmd.Flags().GetBool("no-edit")
	a.WithNoEdit(noEdit)
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

// WithNoEdit skips the interactive edit of an install's editable inputs.
func (a *Add) WithNoEdit(noEdit bool) *Add {
	a.NoEdit = noEdit
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

// WithRESTConfig sets the cluster connection config. Optional: when unset,
// Run infers it from the kubeconfig.
func (a *Add) WithRESTConfig(config *rest.Config) *Add {
	a.RESTConfig = config
	return a
}

// WithContext sets the context carrying cancellation and deadlines into Run.
func (a *Add) WithContext(ctx context.Context) *Add {
	a.Context = ctx
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

	// Infer the connection from the kubeconfig when none was provided, so
	// WithRESTConfig is optional for library callers.
	if a.RESTConfig == nil {
		flags := a.ConfigFlags
		if flags == nil {
			flags = genericclioptions.NewConfigFlags(true)
			a.ConfigFlags = flags
		}
		config, err := flags.ToRESTConfig()
		if err != nil {
			return fmt.Errorf("inferring REST config from kubeconfig: %w", err)
		}
		a.RESTConfig = config
		if a.Namespace == "" {
			if ns, _, err := flags.ToRawKubeConfigLoader().Namespace(); err == nil {
				a.WithNamespace(ns)
			}
		}
	}

	ctx := a.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// A helm chart is discovered, rendered, and (optionally) has its
	// values staged — a flow distinct from fetching a single manifest.
	if a.Format == resolve.FormatHelm {
		return a.installHelm(ctx, manifest)
	}

	slog.Info("fetching", "url", manifest)
	body, err := a.fetch(ctx, manifest)
	if err != nil {
		return err
	}

	var kustomizeDir string
	switch a.Format {
	case resolve.FormatYAML:
		// Applied as-is.
	case resolve.FormatKustomize:
		// Built server-side; the referenced tree (relative and in-site
		// ../ resources, nested kustomizations) is materialized into a
		// tar the builder unpacks, leaving the kustomization untouched.
		body, kustomizeDir, err = kustomize.Materialize(ctx, body, manifest, a.fetch)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("resolved %s to %s (%s): installing %s is not implemented yet", a.Resource, manifest, a.Format, a.Format)
	}

	return a.apply(ctx, manifest, body, a.Format, kustomizeDir)
}

// installHelm discovers the chart, reconciles its values ConfigMap (opening
// an interactive edit unless suppressed), renders with those values, and
// applies the result.
func (a *Add) installHelm(ctx context.Context, chartURL *url.URL) error {
	client, err := kubernetes.NewForConfig(a.RESTConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	slog.Info("discovering chart", "url", chartURL)
	var chart *helm.Chart
	switch {
	case chartURL.Scheme == "oci" || strings.HasSuffix(chartURL.Path, ".tgz"):
		// A packaged chart (OCI registry or .tgz), pulled and loaded directly.
		chart, err = helm.DiscoverArchive(ctx, chartURL, a.get)
	case path.Base(chartURL.Path) == "Chart.yaml" || path.Base(chartURL.Path) == "Chart.yml":
		// Loose chart files served over HTTP, discovered by convention.
		chart, err = helm.Discover(ctx, chartURL, a.get)
	default:
		// A chart repository, resolved through its index.yaml.
		chart, err = helm.DiscoverRepo(ctx, chartURL, a.get)
	}
	if err != nil {
		return err
	}

	// Key the values on the chart's identity minus its version, so edited
	// values persist across version bumps (?chart= stays; ?version= drops).
	valuesID := *chartURL
	if q := valuesID.Query(); q.Has("version") {
		q.Del("version")
		valuesID.RawQuery = q.Encode()
	}
	valuesName := helm.ValuesName(valuesID.String())

	values, exists, err := helm.LoadValues(ctx, client, a.Namespace, valuesName)
	if err != nil {
		return err
	}
	if !exists {
		values = chart.DefaultValues
		if err := helm.StoreValues(ctx, client, a.Namespace, valuesName, values); err != nil {
			return err
		}
		slog.Debug("persisted default values", "configmap", valuesName)
	} else {
		slog.Info("using persisted values", "configmap", valuesName)
	}

	// Let the user review/edit the reconciled values before install. Skipped
	// with --no-edit or when stdin is not a terminal (scripts, CI).
	if !a.NoEdit && term.IsTerminal(int(os.Stdin.Fd())) {
		if err := a.editConfigMap(ctx, valuesName); err != nil {
			return err
		}
		if values, _, err = helm.LoadValues(ctx, client, a.Namespace, valuesName); err != nil {
			return err
		}
	}

	// Render against the target cluster's version so charts with a
	// kubeVersion constraint (and version-gated templates) resolve
	// correctly, rather than helm's stale client-only default.
	kubeVersion := ""
	if sv, err := client.Discovery().ServerVersion(); err != nil {
		slog.Debug("could not determine cluster version; using helm default", "err", err)
	} else {
		kubeVersion = sv.GitVersion
	}

	release := helm.ReleaseName(chart.Chart)
	slog.Info("rendering chart", "release", release, "kubeVersion", kubeVersion)
	rendered, err := helm.Render(chart.Chart, values, release, a.Namespace, kubeVersion)
	if err != nil {
		return err
	}

	// The chart is rendered to plain yaml; apply it like any manifest.
	return a.apply(ctx, chartURL, rendered, resolve.FormatYAML, "")
}

// editConfigMap opens the named ConfigMap in the user's editor via
// `kubectl edit`, forwarding the caller's connection flags so it targets the
// same cluster and context. The editor takes over stdio.
func (a *Add) editConfigMap(ctx context.Context, name string) error {
	args := []string{"edit", "configmap", name, "--namespace", a.Namespace}
	if f := a.ConfigFlags; f != nil {
		if f.KubeConfig != nil && *f.KubeConfig != "" {
			args = append(args, "--kubeconfig", *f.KubeConfig)
		}
		if f.Context != nil && *f.Context != "" {
			args = append(args, "--context", *f.Context)
		}
	}

	slog.Info("editing values", "configmap", name, "namespace", a.Namespace)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editing values %s: %w", name, err)
	}
	return nil
}

// apply streams the manifest to the server-side applier.
func (a *Add) apply(ctx context.Context, source *url.URL, body []byte, format resolve.Format, kustomizeDir string) error {
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
	slog.Info(action, "url", source)
	return remote.New().
		WithRESTConfig(a.RESTConfig).
		WithNamespace(a.Namespace).
		WithManifest(body).
		WithFormat(format).
		WithDir(kustomizeDir).
		WithVerbosity(verbosity).
		WithRemove(a.Remove).
		WithConfigFlags(a.ConfigFlags).
		WithStreams(os.Stdout, os.Stderr).
		Run(ctx)
}

// maxRedirects bounds the redirect chain when fetching a manifest.
const maxRedirects = 10

// fetch downloads the resource at u, erroring if it is absent.
func (a *Add) fetch(ctx context.Context, u *url.URL) ([]byte, error) {
	body, found, err := a.get(ctx, u)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("fetching %s: not found", u)
	}
	return body, nil
}

// get downloads the resource at u, following redirects (e.g. k8s.io short
// links to raw content) up to maxRedirects hops. A 404 is reported as
// found=false rather than an error, so callers can probe optional files.
func (a *Add) get(ctx context.Context, u *url.URL) ([]byte, bool, error) {
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
		return nil, false, fmt.Errorf("building request for %s: %w", u, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("fetching %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("fetching %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", u, err)
	}
	return body, true, nil
}
