//go:build e2e

// Package e2e drives the built kubectl-add binary against a live cluster,
// applying and removing the repo's e2e manifests straight from the
// published GitHub Pages site and verifying the results with client-go.
//
// Run with: make test-e2e  (or: go test -tags e2e ./test/e2e/ -v -timeout 15m)
// Requires a reachable cluster via the standard kubeconfig.
package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const namespace = "kubectl-add-e2e"

var (
	binPath   string
	clientset *kubernetes.Clientset
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// run does setup/teardown around the tests, returning the exit code. A
// separate function so deferred cleanup runs before os.Exit.
func run(m *testing.M) int {
	root := repoRoot()

	dir, err := os.MkdirTemp("", "kubectl-add-e2e")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "kubectl-add")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = root
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		panic("building binary: " + err.Error())
	}

	clientset = connectCluster()
	if clientset != nil {
		ensureNamespace()
		defer deleteNamespace()
	}

	return m.Run()
}

func TestExamples(t *testing.T) {
	if clientset == nil {
		t.Skip("no reachable cluster (set KUBECONFIG / current-context)")
	}

	cases := []struct {
		url    string
		verify func(t *testing.T, wantExists bool)
	}{
		{
			url: "https://scaffoldly.github.io/kubectl-add/yaml/nginx.yaml",
			verify: func(t *testing.T, wantExists bool) {
				assertExists(t, wantExists, "deployment/nginx", func(ctx context.Context) error {
					_, err := clientset.AppsV1().Deployments(namespace).Get(ctx, "nginx", metav1.GetOptions{})
					return err
				})
				assertExists(t, wantExists, "service/nginx", func(ctx context.Context) error {
					_, err := clientset.CoreV1().Services(namespace).Get(ctx, "nginx", metav1.GetOptions{})
					return err
				})
			},
		},
		{
			url: "https://scaffoldly.github.io/kubectl-add/yaml/configmap.yaml",
			verify: func(t *testing.T, wantExists bool) {
				assertExists(t, wantExists, "configmap/hello", func(ctx context.Context) error {
					_, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, "hello", metav1.GetOptions{})
					return err
				})
			},
		},
		{
			url: "https://scaffoldly.github.io/kubectl-add/kustomization/kustomization.yaml",
			verify: func(t *testing.T, wantExists bool) {
				// nginx comes in through a base; the top kustomization
				// patches it with the e2e=bases label, so a present
				// deployment must also carry that label.
				assertExists(t, wantExists, "deployment/nginx (label e2e=bases)", func(ctx context.Context) error {
					d, err := clientset.AppsV1().Deployments(namespace).Get(ctx, "nginx", metav1.GetOptions{})
					if err != nil {
						return err
					}
					if d.Labels["e2e"] != "bases" {
						return fmt.Errorf("deployment/nginx missing label e2e=bases, got %v", d.Labels)
					}
					return nil
				})
				assertExists(t, wantExists, "service/nginx", func(ctx context.Context) error {
					_, err := clientset.CoreV1().Services(namespace).Get(ctx, "nginx", metav1.GetOptions{})
					return err
				})
				assertExists(t, wantExists, "configmap/hello", func(ctx context.Context) error {
					_, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, "hello", metav1.GetOptions{})
					return err
				})
				assertExists(t, wantExists, "secret/hello-secret", func(ctx context.Context) error {
					_, err := clientset.CoreV1().Secrets(namespace).Get(ctx, "hello-secret", metav1.GetOptions{})
					return err
				})
			},
		},
		{
			url: "https://scaffoldly.github.io/kubectl-add/helm/Chart.yaml",
			verify: func(t *testing.T, wantExists bool) {
				assertExists(t, wantExists, "configmap/hello-config", func(ctx context.Context) error {
					_, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, "hello-config", metav1.GetOptions{})
					return err
				})
			},
		},
		{
			// Chart repository: sniff index.yaml, pick the chart's latest
			// version, pull the packaged .tgz, render, apply.
			url: "https://scaffoldly.github.io/kubectl-add/helm-repo",
			verify: func(t *testing.T, wantExists bool) {
				assertExists(t, wantExists, "configmap/hello-config", func(ctx context.Context) error {
					_, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, "hello-config", metav1.GetOptions{})
					return err
				})
			},
		},
		{
			// Chart repository with an explicit chart and version pinned via
			// query params.
			url: "https://scaffoldly.github.io/kubectl-add/helm-repo?chart=hello&version=0.1.0",
			verify: func(t *testing.T, wantExists bool) {
				assertExists(t, wantExists, "configmap/hello-config", func(ctx context.Context) error {
					_, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, "hello-config", metav1.GetOptions{})
					return err
				})
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			runAdd(t, tc.url)
			tc.verify(t, true)

			runAdd(t, "--remove", tc.url)
			tc.verify(t, false)
		})
	}
}

// runAdd executes the binary with the given args, scoped to the e2e
// namespace, failing the test on non-zero exit.
func runAdd(t *testing.T, args ...string) {
	t.Helper()
	args = append(args, "--namespace", namespace)
	cmd := exec.Command(binPath, args...)
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	t.Logf("kubectl-add %v\n%s", args, out)
	if err != nil {
		t.Fatalf("kubectl-add %v failed: %v", args, err)
	}
}

// assertExists polls get until the resource's presence matches wantExists.
func assertExists(t *testing.T, wantExists bool, name string, get func(ctx context.Context) error) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := get(context.Background())
		switch {
		case wantExists && err == nil:
			return
		case !wantExists && apierrors.IsNotFound(err):
			return
		case err != nil && !apierrors.IsNotFound(err):
			t.Fatalf("%s: get error: %v", name, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: still exists=%v after 60s (want exists=%v)", name, err == nil, wantExists)
		}
		time.Sleep(time.Second)
	}
}

func connectCluster() *kubernetes.Clientset {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil
	}
	if _, err := client.Discovery().ServerVersion(); err != nil {
		return nil
	}
	return client
}

func ensureNamespace() {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	_, _ = clientset.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{})
}

func deleteNamespace() {
	_ = clientset.CoreV1().Namespaces().Delete(context.Background(), namespace, metav1.DeleteOptions{})
}

// repoRoot returns the module root, two levels up from this test file.
func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}
