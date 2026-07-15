// Package remote runs kubectl apply inside the cluster: it delivers the
// manifest through a ConfigMap, runs a short-lived pod carrying an
// impersonation-only ServiceAccount, and execs kubectl into it impersonating
// the local caller so the apply is authorized and audited as the user.
package remote

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

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
	// manifestPath is where the ConfigMap is mounted in the runner pod.
	manifestPath = "/manifests"
	// manifestKey is the ConfigMap key holding the manifest.
	manifestKey = "manifest.yaml"
	// kubeconfigKey is the ConfigMap key holding the in-cluster kubeconfig.
	kubeconfigKey = "kubeconfig"

	saName          = "kubectl-add"
	impersonateRole = "kubectl-add:impersonator"

	// inClusterKubeconfig points kubectl at the API server using the
	// runner's auto-mounted ServiceAccount token. Identity beyond this
	// base auth comes from the --as impersonation flags.
	inClusterKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: in-cluster
  cluster:
    server: https://kubernetes.default.svc
    certificate-authority: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
users:
- name: in-cluster
  user:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
contexts:
- name: in-cluster
  context:
    cluster: in-cluster
    user: in-cluster
current-context: in-cluster
`
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
	// ConfigFlags supplies kubectl's standard flags; the request-scoped
	// ones (namespace, request-timeout) are forwarded to the remote
	// kubectl. Connection/auth flags are not: the remote connects
	// in-cluster.
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

	user, groups, err := a.whoami(ctx)
	if err != nil {
		return err
	}
	slog.Info("impersonating", "user", user, "groups", len(groups))

	if err := a.ensureRBAC(ctx); err != nil {
		return err
	}

	runID := rand.String(5)
	name := "kubectl-add-" + runID

	if err := a.createConfigMap(ctx, name); err != nil {
		return err
	}
	defer a.deleteConfigMap(name)

	if err := a.createPod(ctx, name); err != nil {
		return err
	}
	defer a.deletePod(name)

	if err := a.waitReady(ctx, name); err != nil {
		return err
	}

	return a.exec(ctx, name, user, groups)
}

// whoami asks the API server who the local credentials authenticate as, so
// the remote kubectl can impersonate the same user and groups.
func (a *Applier) whoami(ctx context.Context) (string, []string, error) {
	review, err := a.client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", nil, fmt.Errorf("remote: SelfSubjectReview: %w", err)
	}
	user := review.Status.UserInfo.Username
	if user == "" {
		return "", nil, fmt.Errorf("remote: could not determine local identity")
	}
	return user, review.Status.UserInfo.Groups, nil
}

// ensureRBAC creates the impersonation-only ServiceAccount, ClusterRole, and
// binding. Idempotent: already-existing objects are left as-is.
func (a *Applier) ensureRBAC(ctx context.Context) error {
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: a.Namespace}}
	if _, err := a.client.CoreV1().ServiceAccounts(a.Namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("remote: creating service account: %w", err)
	}

	role := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: impersonateRole},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"users", "groups", "serviceaccounts"},
			Verbs:     []string{"impersonate"},
		}},
	}
	if _, err := a.client.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("remote: creating cluster role: %w", err)
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: impersonateRole + ":" + a.Namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: impersonateRole},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: a.Namespace}},
	}
	if _, err := a.client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("remote: creating cluster role binding: %w", err)
	}

	slog.Debug("ensured impersonation rbac", "serviceaccount", saName, "namespace", a.Namespace)
	return nil
}

func (a *Applier) createConfigMap(ctx context.Context, name string) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace},
		Data: map[string]string{
			manifestKey:   string(a.Manifest),
			kubeconfigKey: inClusterKubeconfig,
		},
	}
	if _, err := a.client.CoreV1().ConfigMaps(a.Namespace).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("remote: creating configmap: %w", err)
	}
	slog.Debug("created manifest configmap", "name", name)
	return nil
}

func (a *Applier) createPod(ctx context.Context, name string) error {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: a.Namespace},
		Spec: corev1.PodSpec{
			ServiceAccountName: saName,
			RestartPolicy:      corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "kubectl",
				Image:   a.RunnerImage,
				Command: []string{"sleep", "3600"},
				VolumeMounts: []corev1.VolumeMount{{
					Name:      "manifest",
					MountPath: manifestPath,
					ReadOnly:  true,
				}},
			}},
			Volumes: []corev1.Volume{{
				Name: "manifest",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: name},
					},
				},
			}},
		},
	}
	if _, err := a.client.CoreV1().Pods(a.Namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("remote: creating pod: %w", err)
	}
	slog.Debug("created runner pod", "name", name, "image", a.RunnerImage)
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

// exec runs kubectl apply inside the runner, impersonating the caller, and
// streams its output back.
func (a *Applier) exec(ctx context.Context, name, user string, groups []string) error {
	verb := "apply"
	if a.Remove {
		verb = "delete"
	}
	command := []string{"kubectl", verb}

	// Layer the caller's kubectl flags first. Connection/auth flags
	// (--server, --context, --kubeconfig, --token, certs) are excluded:
	// the remote kubectl connects in-cluster.
	if f := a.ConfigFlags; f != nil {
		if f.Namespace != nil && *f.Namespace != "" {
			command = append(command, "--namespace", *f.Namespace)
		}
		if f.Timeout != nil && *f.Timeout != "" && *f.Timeout != "0" {
			command = append(command, "--request-timeout", *f.Timeout)
		}
		if f.Impersonate != nil && *f.Impersonate != "" {
			command = append(command, "--as", *f.Impersonate)
		}
		if f.ImpersonateGroup != nil {
			for _, group := range *f.ImpersonateGroup {
				command = append(command, "--as-group", group)
			}
		}
		if f.ImpersonateUID != nil && *f.ImpersonateUID != "" {
			command = append(command, "--as-uid", *f.ImpersonateUID)
		}
	}

	// Then our execution flags, which override the caller's on conflict:
	// the in-cluster kubeconfig, target namespace, and resolved identity.
	command = append(command,
		"--kubeconfig", manifestPath+"/"+kubeconfigKey,
		"-f", manifestPath+"/"+manifestKey,
		"--namespace", a.Namespace,
		"--as", user,
	)
	for _, group := range groups {
		command = append(command, "--as-group", group)
	}
	if a.Verbosity > 0 {
		command = append(command, fmt.Sprintf("-v=%d", a.Verbosity))
	}
	slog.Debug("exec in runner", "pod", name, "command", command)

	req := a.client.CoreV1().RESTClient().Post().
		Resource("pods").Name(name).Namespace(a.Namespace).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Command: command,
			Stdout:  true,
			Stderr:  true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(a.RESTConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("remote: building executor: %w", err)
	}
	if err := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: a.Out, Stderr: a.Err}); err != nil {
		return fmt.Errorf("remote: applying manifest: %w", err)
	}
	return nil
}

func (a *Applier) deletePod(name string) {
	if err := a.client.CoreV1().Pods(a.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cleanup: delete pod", "name", name, "error", err)
	}
}

func (a *Applier) deleteConfigMap(name string) {
	if err := a.client.CoreV1().ConfigMaps(a.Namespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		slog.Debug("cleanup: delete configmap", "name", name, "error", err)
	}
}
