// Package remote runs kubectl apply inside the cluster: it mints a
// short-lived ServiceAccount token into a Secret, runs a short-lived pod
// that authenticates with that token (fully file-less — no kubeconfig, just
// --server/--certificate-authority/--token flags), and streams the manifest
// to kubectl over the exec's stdin.
package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/scaffoldly/kubectl-add/v1alpha1/resolve"
	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

const (
	// DefaultRunnerImage carries kubectl; override with WithRunnerImage.
	DefaultRunnerImage = "bitnami/kubectl:latest"
	// apiServer is the in-cluster API server address.
	apiServer = "https://kubernetes.default.svc"
	// secretPath is where the token/CA Secret is mounted in the pod.
	secretPath = "/auth"
	// tokenKey and caKey are the Secret keys holding the credentials.
	tokenKey = "token"
	caKey    = "ca.crt"
	// tokenTTL bounds the minted ServiceAccount token's lifetime.
	tokenTTL = 10 * time.Minute

	saName      = "kubectl-add"
	adminRole   = "cluster-admin"
	bindingName = "kubectl-add:apply"
)

// Applier runs a server-side kubectl apply of Manifest.
type Applier struct {
	RESTConfig  *rest.Config
	Namespace   string
	Manifest    []byte
	RunnerImage string
	// Verbosity, when > 0, is passed to the remote kubectl as -v=N.
	Verbosity int
	// Remove deletes the manifest's resources instead of applying them.
	Remove bool
	// Format is the manifest's artifact format. A kustomization is built
	// server-side before applying; yaml is applied as-is.
	Format resolve.Format
	// Dir is the directory within the streamed tar to operate in.
	Dir string
	// ConfigFlags supplies kubectl's standard flags; the request-scoped
	// ones (namespace, request-timeout) are forwarded to the remote
	// kubectl. Connection/auth flags are not: the remote authenticates
	// with the minted ServiceAccount token.
	ConfigFlags *genericclioptions.ConfigFlags

	// Out/Err receive the remote kubectl's streams.
	Out io.Writer
	Err io.Writer

	client *kubernetes.Clientset
	err    error
}

func New() *Applier {
	return &Applier{
		RunnerImage: DefaultRunnerImage,
	}
}

func (a *Applier) WithRESTConfig(config *rest.Config) *Applier {
	a.RESTConfig = config
	return a
}

// WithNamespace sets the namespace the runner and its resources live in.
func (a *Applier) WithNamespace(namespace string) *Applier {
	a.Namespace = namespace
	return a
}

func (a *Applier) WithManifest(manifest []byte) *Applier {
	a.Manifest = manifest
	return a
}

// WithRunnerImage overrides the kubectl image, falling back to
// DefaultRunnerImage when empty.
func (a *Applier) WithRunnerImage(image string) *Applier {
	if image == "" {
		image = DefaultRunnerImage
	}
	a.RunnerImage = image
	return a
}

func (a *Applier) WithStreams(out, err io.Writer) *Applier {
	a.Out = out
	a.Err = err
	return a
}

// WithVerbosity sets the remote kubectl log level (-v=N); 0 omits the flag.
func (a *Applier) WithVerbosity(v int) *Applier {
	a.Verbosity = v
	return a
}

// WithRemove selects kubectl delete instead of apply.
func (a *Applier) WithRemove(remove bool) *Applier {
	a.Remove = remove
	return a
}

// WithFormat sets the manifest's artifact format, selecting how the runner
// installs it.
func (a *Applier) WithFormat(format resolve.Format) *Applier {
	a.Format = format
	return a
}

// WithDir sets the directory within the streamed tar to operate in.
func (a *Applier) WithDir(dir string) *Applier {
	a.Dir = dir
	return a
}

// WithConfigFlags supplies kubectl's standard flags for forwarding the
// request-scoped ones to the remote kubectl.
func (a *Applier) WithConfigFlags(flags *genericclioptions.ConfigFlags) *Applier {
	a.ConfigFlags = flags
	return a
}

func (a *Applier) Run(ctx context.Context) error {
	if a.err != nil {
		return a.err
	}
	if a.RESTConfig == nil {
		return fmt.Errorf("remote: no REST config")
	}
	if len(a.Manifest) == 0 {
		return fmt.Errorf("remote: empty manifest")
	}

	client, err := kubernetes.NewForConfig(a.RESTConfig)
	if err != nil {
		return fmt.Errorf("remote: building clientset: %w", err)
	}
	a.client = client

	if err := a.ensureRBAC(ctx); err != nil {
		return err
	}

	token, err := a.mintToken(ctx)
	if err != nil {
		return err
	}

	ca, err := a.caCert()
	if err != nil {
		return err
	}

	base := "kubectl-add-" + rand.String(5)

	if err := a.createSecret(ctx, base, token, ca); err != nil {
		return err
	}
	defer a.deleteSecret(base)

	// The applier pod carries the minted token and runs kubectl apply.
	applier := base
	if err := a.createPod(ctx, applier, base); err != nil {
		return err
	}
	defer a.deletePod(applier)

	if a.Format != resolve.FormatKustomize {
		if err := a.waitReady(ctx, applier); err != nil {
			return err
		}
		return a.apply(ctx, applier, bytes.NewReader(a.Manifest))
	}

	// The builder pod runs kubectl kustomize (no cluster credentials, just
	// network egress to fetch remote resources). This binary pipes the
	// builder's stdout into the applier's stdin.
	builder := base + "-build"
	if err := a.createPod(ctx, builder, ""); err != nil {
		return err
	}
	defer a.deletePod(builder)

	if err := a.waitReady(ctx, applier); err != nil {
		return err
	}
	if err := a.waitReady(ctx, builder); err != nil {
		return err
	}
	return a.pipeKustomize(ctx, builder, applier)
}

// pipeKustomize builds the streamed kustomization in the builder pod and
// pipes its rendered output into the applier pod: this binary is the pipe.
func (a *Applier) pipeKustomize(ctx context.Context, builder, applier string) error {
	pr, pw := io.Pipe()

	buildErr := make(chan error, 1)
	go func() {
		// Unpack the streamed kustomization tree, build the kustomization
		// dir, write the rendered manifest to the pipe for the applier.
		// The dir arrives as $1 (real argv) so it can't be reinterpreted
		// by the shell.
		script := `d=$(mktemp -d) && tar -x -C "$d" && exec kubectl kustomize "$d/$1"`
		command := []string{"sh", "-c", script, "sh", a.Dir}
		err := a.stream(ctx, builder, command, bytes.NewReader(a.Manifest), pw, a.Err)
		pw.CloseWithError(err)
		buildErr <- err
	}()

	applyErr := a.apply(ctx, applier, pr)
	if err := <-buildErr; err != nil {
		return fmt.Errorf("remote: building kustomization: %w", err)
	}
	return applyErr
}

// ensureRBAC creates the runner ServiceAccount and binds it to cluster-admin
// so its minted token can apply arbitrary manifests. Idempotent.
func (a *Applier) ensureRBAC(ctx context.Context) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: a.Namespace}}
	if _, err := a.client.CoreV1().ServiceAccounts(a.Namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("remote: creating service account: %w", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: bindingName + ":" + a.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: adminRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: a.Namespace}},
	}
	if _, err := a.client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("remote: creating cluster role binding: %w", err)
	}

	slog.Debug("ensured runner rbac", "serviceaccount", saName, "role", adminRole, "namespace", a.Namespace)
	return nil
}

// mintToken requests a short-lived token for the runner ServiceAccount via
// the TokenRequest API.
func (a *Applier) mintToken(ctx context.Context) (string, error) {
	seconds := int64(tokenTTL.Seconds())
	req := &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &seconds}}
	resp, err := a.client.CoreV1().ServiceAccounts(a.Namespace).CreateToken(ctx, saName, req, metav1.CreateOptions{})
	if err != nil {
		return "", fmt.Errorf("remote: minting token: %w", err)
	}
	slog.Info("minted token", "serviceaccount", saName, "ttl", tokenTTL.String())
	return resp.Status.Token, nil
}

// caCert returns the cluster CA bundle from the local REST config, used by
// the remote kubectl to verify the API server.
func (a *Applier) caCert() ([]byte, error) {
	if len(a.RESTConfig.CAData) > 0 {
		return a.RESTConfig.CAData, nil
	}
	if a.RESTConfig.CAFile != "" {
		ca, err := os.ReadFile(a.RESTConfig.CAFile)
		if err != nil {
			return nil, fmt.Errorf("remote: reading CA file: %w", err)
		}
		return ca, nil
	}
	return nil, fmt.Errorf("remote: no cluster CA in REST config")
}

// createSecret stores the minted token and CA bundle. The token is injected
// into the pod as an env var (not argv), so it never appears in process
// listings or logs.
func (a *Applier) createSecret(ctx context.Context, name string, token string, ca []byte) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			tokenKey: []byte(token),
			caKey:    ca,
		},
	}
	if _, err := a.client.CoreV1().Secrets(a.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("remote: creating secret: %w", err)
	}
	slog.Debug("created auth secret", "name", name)
	return nil
}

// createPod starts a sleeping kubectl runner. When secretName is non-empty
// the pod mounts that Secret's CA and injects its token as $TOKEN, for the
// applier; an empty secretName yields a credential-less pod, for the builder.
func (a *Applier) createPod(ctx context.Context, name, secretName string) error {
	noAutomount := false
	container := corev1.Container{
		Name:    "kubectl",
		Image:   a.RunnerImage,
		Command: []string{"sleep", "3600"},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &noAutomount,
			RestartPolicy:                corev1.RestartPolicyNever,
		},
	}

	if secretName != "" {
		// The applier authenticates with the minted token from the
		// Secret, not the pod's own projected SA token.
		container.Env = []corev1.EnvVar{{
			Name: "TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  tokenKey,
				},
			},
		}}
		container.VolumeMounts = []corev1.VolumeMount{{
			Name:      "auth",
			MountPath: secretPath,
			ReadOnly:  true,
		}}
		pod.Spec.Volumes = []corev1.Volume{{
			Name: "auth",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: secretName,
					Items:      []corev1.KeyToPath{{Key: caKey, Path: caKey}},
				},
			},
		}}
	}

	pod.Spec.Containers = []corev1.Container{container}
	if _, err := a.client.CoreV1().Pods(a.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("remote: creating pod: %w", err)
	}
	slog.Debug("created runner pod", "name", name, "image", a.RunnerImage, "authed", secretName != "")
	return nil
}

func (a *Applier) waitReady(ctx context.Context, name string) error {
	slog.Info("starting runner", "pod", name)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		pod, err := a.client.CoreV1().Pods(a.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("remote: waiting for pod: %w", err)
		}
		if pod.Status.Phase == corev1.PodRunning && podReady(pod) {
			slog.Debug("runner ready", "pod", name)
			return nil
		}
		if pod.Status.Phase == corev1.PodFailed {
			return fmt.Errorf("remote: runner pod %s failed", name)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("remote: runner pod %s not ready after 2m (phase %s)", name, pod.Status.Phase)
		}
		time.Sleep(time.Second)
	}
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return false
		}
	}
	return len(pod.Status.ContainerStatuses) > 0
}

// apply runs kubectl apply (or delete) in the applier pod, file-less: it
// authenticates with the minted token (via $TOKEN) and the mounted CA, and
// reads the manifest from stdin. The fixed connection flags plus "$@" run
// through sh so $TOKEN is expanded from the environment; caller args arrive
// as real argv via "$@", so they cannot be reinterpreted by the shell.
func (a *Applier) apply(ctx context.Context, pod string, manifest io.Reader) error {
	verb := "apply"
	if a.Remove {
		verb = "delete"
	}
	script := fmt.Sprintf(`exec kubectl %s --server=%s --certificate-authority=%s/%s --token="$TOKEN" "$@"`,
		verb, apiServer, secretPath, caKey)

	args := []string{"sh", "-c", script, "sh"}

	// Caller's request-scoped flags first; our execution flags override.
	if f := a.ConfigFlags; f != nil {
		if f.Timeout != nil && *f.Timeout != "" && *f.Timeout != "0" {
			args = append(args, "--request-timeout", *f.Timeout)
		}
	}
	args = append(args, "-f", "-", "--namespace", a.Namespace)
	if a.Verbosity > 0 {
		args = append(args, fmt.Sprintf("-v=%d", a.Verbosity))
	}
	slog.Debug("apply in runner", "pod", pod, "args", args)

	if err := a.stream(ctx, pod, args, manifest, a.Out, a.Err); err != nil {
		return fmt.Errorf("remote: applying manifest: %w", err)
	}
	return nil
}

// stream execs command in pod, wiring the given stdin/stdout/stderr to the
// remote process.
func (a *Applier) stream(ctx context.Context, pod string, command []string, stdin io.Reader, stdout, stderr io.Writer) error {
	req := a.client.CoreV1().RESTClient().Post().
		Resource("pods").Name(pod).Namespace(a.Namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdin:   stdin != nil,
			Stdout:  stdout != nil,
			Stderr:  stderr != nil,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(a.RESTConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("remote: building executor: %w", err)
	}
	return executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	})
}

func (a *Applier) deletePod(name string) {
	if err := a.client.CoreV1().Pods(a.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cleanup: delete pod", "name", name, "error", err)
	}
}

func (a *Applier) deleteSecret(name string) {
	if err := a.client.CoreV1().Secrets(a.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cleanup: delete secret", "name", name, "error", err)
	}
}
