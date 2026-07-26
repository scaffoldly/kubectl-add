// Package deployment manages the persistent in-cluster tunnel: a libtunnel
// Deployment that dials Cloudflare (LIBTUNNEL__CLOUDFLARE=1) with a target as
// its local origin (LIBTUNNEL_LOCAL_URL). Install deploys it and reads back the
// public URL; Remove deletes it.
package deployment

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/cnuss/libtunnel"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "kubectl-add"
	componentLabel = "app.kubernetes.io/component"
	componentValue = "tunnel"
	targetLabel    = "kubectl-add/tunnel-target"

	readyTimeout = 90 * time.Second
)

// publicURLRe matches the Cloudflare quick-tunnel URL the container prints.
var publicURLRe = regexp.MustCompile(`https://\S+\.trycloudflare\.com\S*`)

// image is the container run by an installed tunnel, pinned to the linked
// library's version so the in-cluster binary matches (each release publishes
// the image tagged with the v-prefixed version Version returns).
func image() string {
	return "ghcr.io/cnuss/libtunnel:" + libtunnel.Version()
}

// Install deploys a persistent in-cluster tunnel for a target and waits for it
// to report a public URL, returning that URL, a human-readable description, and
// the in-cluster origin the container forwards to.
func Install(ctx context.Context, cfg *rest.Config, namespace, name, port string, isService bool, log *slog.Logger) (publicURL, describe, origin string, err error) {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return "", "", "", fmt.Errorf("building clientset: %w", err)
	}

	origin, describe, err = originURL(namespace, name, port, isService)
	if err != nil {
		return "", "", "", err
	}
	if !isService {
		log.Warn("installing a public tunnel to the API server", "note", "anyone with the URL can reach it (they still authenticate)")
	}

	dep := build(namespace, name, isService, origin)
	if err := apply(ctx, client, namespace, dep); err != nil {
		return "", "", "", fmt.Errorf("installing tunnel: %w", err)
	}
	log.Info("installed tunnel", "target", describe, "deployment", dep.Name, "origin", origin)

	selector := fmt.Sprintf("%s=%s,%s=%s", managedByLabel, managedByValue, targetLabel, dep.Labels[targetLabel])
	publicURL, err = waitForPublicURL(ctx, client, namespace, selector)
	if err != nil {
		return "", "", "", err
	}
	return publicURL, describe, origin, nil
}

// Remove deletes an installed tunnel's Deployment.
func Remove(ctx context.Context, cfg *rest.Config, namespace, name string, isService bool, log *slog.Logger) error {
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("building clientset: %w", err)
	}

	describe := "apiserver (kubernetes.default.svc)"
	if isService {
		// A missing port is fine for removal — the name locates the workload.
		describe = fmt.Sprintf("service %s/%s", namespace, name)
	}

	depName := name_(name, isService)
	err = client.AppsV1().Deployments(namespace).Delete(ctx, depName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("no installed tunnel found for %s in namespace %s", describe, namespace)
	}
	if err != nil {
		return fmt.Errorf("removing tunnel: %w", err)
	}
	log.Info("removed tunnel", "target", describe, "deployment", depName)
	fmt.Fprintf(os.Stdout, "removed tunnel %s\n", describe)
	return nil
}

// originURL resolves the in-cluster origin URL the deployed container dials,
// plus a human-readable description. The API server target dials
// kubernetes.default; a Service dials its cluster DNS name and requires a port.
func originURL(namespace, name, port string, isService bool) (origin, describe string, err error) {
	if !isService {
		return "https://kubernetes.default.svc:443", "apiserver (kubernetes.default.svc)", nil
	}
	if port == "" {
		return "", "", fmt.Errorf("a service install needs a port: svc/%s:<port>", name)
	}
	origin = fmt.Sprintf("http://%s.%s.svc:%s", name, namespace, port)
	return origin, fmt.Sprintf("service %s/%s:%s", namespace, name, port), nil
}

// build builds the libtunnel Deployment for a target.
func build(namespace, name string, isService bool, origin string) *appsv1.Deployment {
	labels := map[string]string{
		managedByLabel: managedByValue,
		componentLabel: componentValue,
		targetLabel:    slug(name, isService),
	}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name_(name, isService),
			Namespace: namespace,
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
						Image: image(),
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

// apply creates the Deployment, updating it in place if one already exists (a
// re-install with a changed image or origin).
func apply(ctx context.Context, client kubernetes.Interface, ns string, dep *appsv1.Deployment) error {
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
func waitForPublicURL(ctx context.Context, client kubernetes.Interface, namespace, selector string) (string, error) {
	var podName string
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, readyTimeout, true, func(ctx context.Context) (bool, error) {
		pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
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

	stream, err := client.CoreV1().Pods(namespace).
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

// name_ is the deterministic Deployment name for a target, so install and
// remove agree and re-install is idempotent.
func name_(name string, isService bool) string {
	return "kubectl-add-tunnel-" + slug(name, isService)
}

// slug is a DNS-1123-safe label/name fragment for a target.
func slug(name string, isService bool) string {
	if !isService {
		return "apiserver"
	}
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		s = "service"
	}
	return s
}

// --- Extension points for planned in-cluster tunnel features (unwired) ---

// secretName is the deterministic Secret name for a target's persisted spec
// (see persistSpec), matching the Deployment's naming scheme.
func secretName(name string, isService bool) string { //nolint:unused
	return "kubectl-add-tunnel-" + slug(name, isService)
}

// persistSpec will save the installed tunnel's minted libtunnel spec — which
// the container exports via LIBTUNNEL_SPEC / LIBTUNNEL_HOSTNAME — into a Secret,
// and inject it back on re-install, so an installed tunnel keeps a stable public
// hostname across pod restarts. See scaffoldly/kubectl-add#46.
func persistSpec(ctx context.Context, client kubernetes.Interface, secret string) error { //nolint:unused
	return fmt.Errorf("persisting the tunnel spec to a Secret is not implemented yet")
}

// updateAPIServerIdentity will reflect an installed API-server tunnel's public
// URL into the relevant in-cluster identity/config resources. See
// scaffoldly/kubectl-add#46.
func updateAPIServerIdentity(ctx context.Context, client kubernetes.Interface, publicURL string) error { //nolint:unused
	return fmt.Errorf("updating API server identity resources is not implemented yet")
}
