package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	// libtunnelImage is the container run by an installed tunnel. Pinned to
	// :latest until the linked library exposes its version (cnuss/libtunnel#100),
	// after which this should track libtunnel.Version().
	libtunnelImage = "ghcr.io/cnuss/libtunnel:latest"

	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "kubectl-add"
	componentLabel = "app.kubernetes.io/component"
	componentValue = "tunnel"
	targetLabel    = "kubectl-add/tunnel-target"

	installReadyTimeout = 90 * time.Second
)

// publicURLRe matches the Cloudflare quick-tunnel URL the container prints.
var publicURLRe = regexp.MustCompile(`https://\S+\.trycloudflare\.com\S*`)

// runInstall deploys a persistent in-cluster tunnel: a libtunnel Deployment
// that dials Cloudflare (LIBTUNNEL__CLOUDFLARE=1) with the target as its local
// origin (LIBTUNNEL_LOCAL_URL). It waits for the pod, reads the public URL from
// its logs, prints it, and leaves the workload running.
func (t *Tunnel) runInstall(ctx context.Context) error {
	client, err := kubernetes.NewForConfig(t.restConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	name, port, isService := parseTarget(t.target)
	origin, describe, err := t.installOrigin(name, port, isService)
	if err != nil {
		return err
	}
	if !isService {
		t.log.Warn("installing a public tunnel to the API server", "note", "anyone with the URL can reach it (they still authenticate)")
	}

	dep := t.deployment(name, isService, origin)
	if err := applyDeployment(ctx, client, t.namespace, dep); err != nil {
		return fmt.Errorf("installing tunnel: %w", err)
	}
	t.log.Info("installed tunnel", "target", describe, "deployment", dep.Name, "origin", origin)

	selector := fmt.Sprintf("%s=%s,%s=%s", managedByLabel, managedByValue, targetLabel, dep.Labels[targetLabel])
	url, err := t.waitForPublicURL(ctx, client, selector)
	if err != nil {
		return err
	}
	t.log.Info("tunnel ready", "target", describe, "url", url)
	fmt.Fprintln(os.Stdout, url)
	return nil
}

// runRemove deletes an installed tunnel's Deployment.
func (t *Tunnel) runRemove(ctx context.Context) error {
	client, err := kubernetes.NewForConfig(t.restConfig)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	name, _, isService := parseTarget(t.target)
	describe := "apiserver (kubernetes.default.svc)"
	if isService {
		// A missing port is fine for removal — the name locates the workload.
		describe = fmt.Sprintf("service %s/%s", t.namespace, name)
	}

	depName := deploymentName(name, isService)
	err = client.AppsV1().Deployments(t.namespace).Delete(ctx, depName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("no installed tunnel found for %s in namespace %s", describe, t.namespace)
	}
	if err != nil {
		return fmt.Errorf("removing tunnel: %w", err)
	}
	t.log.Info("removed tunnel", "target", describe, "deployment", depName)
	fmt.Fprintf(os.Stdout, "removed tunnel %s\n", describe)
	return nil
}

// installOrigin resolves the in-cluster origin URL the deployed container
// dials, plus a human-readable description. The API server target dials
// kubernetes.default; a Service dials its cluster DNS name and requires a port.
func (t *Tunnel) installOrigin(name, port string, isService bool) (origin, describe string, err error) {
	if !isService {
		return "https://kubernetes.default.svc:443", "apiserver (kubernetes.default.svc)", nil
	}
	if port == "" {
		return "", "", fmt.Errorf("a service install needs a port: svc/%s:<port>", name)
	}
	origin = fmt.Sprintf("http://%s.%s.svc:%s", name, t.namespace, port)
	return origin, fmt.Sprintf("service %s/%s:%s", t.namespace, name, port), nil
}

// deployment builds the libtunnel Deployment for a target.
func (t *Tunnel) deployment(name string, isService bool, origin string) *appsv1.Deployment {
	labels := map[string]string{
		managedByLabel: managedByValue,
		componentLabel: componentValue,
		targetLabel:    targetSlug(name, isService),
	}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName(name, isService),
			Namespace: t.namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "libtunnel",
						Image: libtunnelImage,
						Env: []corev1.EnvVar{
							{Name: "LIBTUNNEL__CLOUDFLARE", Value: "1"},
							{Name: "LIBTUNNEL_LOCAL_URL", Value: origin},
						},
					}},
				},
			},
		},
	}
}

// applyDeployment creates the Deployment, updating it in place if one already
// exists (a re-install with a changed image or origin).
func applyDeployment(ctx context.Context, client kubernetes.Interface, ns string, dep *appsv1.Deployment) error {
	deployments := client.AppsV1().Deployments(ns)
	_, err := deployments.Create(ctx, dep, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, gerr := deployments.Get(ctx, dep.Name, metav1.GetOptions{})
		if gerr != nil {
			return gerr
		}
		dep.ResourceVersion = existing.ResourceVersion
		_, err = deployments.Update(ctx, dep, metav1.UpdateOptions{})
	}
	return err
}

// waitForPublicURL waits for a running pod matching selector, then streams its
// logs until the container reports the Cloudflare public URL.
func (t *Tunnel) waitForPublicURL(ctx context.Context, client kubernetes.Interface, selector string) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, installReadyTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(t.namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}
		for _, p := range pods.Items {
			switch p.Status.Phase {
			case corev1.PodRunning:
				podName = p.Name
				return true, nil
			case corev1.PodFailed:
				return false, fmt.Errorf("tunnel pod %s failed", p.Name)
			}
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("waiting for tunnel pod: %w", err)
	}

	stream, err := client.CoreV1().Pods(t.namespace).
		GetLogs(podName, &corev1.PodLogOptions{Follow: true}).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("reading tunnel pod logs: %w", err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		if url := publicURLRe.FindString(scanner.Text()); url != "" {
			return url, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading tunnel pod logs: %w", err)
	}
	return "", fmt.Errorf("tunnel pod %s did not report a public URL", podName)
}

// deploymentName is the deterministic Deployment name for a target, so install
// and remove agree and re-install is idempotent.
func deploymentName(name string, isService bool) string {
	return "kubectl-add-tunnel-" + targetSlug(name, isService)
}

// targetSlug is a DNS-1123-safe label/name fragment for a target.
func targetSlug(name string, isService bool) string {
	if !isService {
		return "apiserver"
	}
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	slug = strings.Trim(slug, "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug == "" {
		slug = "service"
	}
	return slug
}
