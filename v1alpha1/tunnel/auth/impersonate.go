package auth

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// tokenTTL bounds the minted ServiceAccount token's lifetime.
const tokenTTL = 24 * time.Hour

// Labels tag the ServiceAccount and RBAC this package mints, so they are
// traceable to kubectl-add's tunnel.
const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "kubectl-add"
	componentLabel = "app.kubernetes.io/component"
	componentValue = "tunnel"
)

// MintImpersonation makes a client-certificate identity usable through the
// tunnel. A client cert is an mTLS credential that terminates at the tunnel
// edge and never reaches the API server, so requests would arrive as
// system:anonymous. Instead this reads the caller's own identity
// (SelfSubjectReview over the original connection), creates a ServiceAccount
// allowed to impersonate exactly that identity, mints a bound token, and
// returns a kubeconfig AuthInfo that authenticates as the SA (a bearer token,
// which traverses the tunnel) and impersonates the caller — so the API server
// sees the original user again.
//
// The returned cleanup deletes the ServiceAccount and its RBAC. It requires the
// caller to hold impersonate-granting permissions (create ClusterRole/binding).
func MintImpersonation(ctx context.Context, config *rest.Config, namespace, publicURL string) (*clientcmdapi.AuthInfo, func(), error) {
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("building clientset: %w", err)
	}

	// Who am I? (username, groups, extra)
	ssr, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authnv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("resolving your identity (SelfSubjectReview): %w", err)
	}
	me := ssr.Status.UserInfo

	saName := serviceAccountName(publicURL)
	labels := map[string]string{
		managedByLabel: managedByValue,
		componentLabel: componentValue,
	}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: namespace, Labels: labels}}
	if _, err := client.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, nil, fmt.Errorf("creating tunnel ServiceAccount: %w", err)
	}

	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Labels: labels},
		Rules:      impersonationRules(me),
	}
	if _, err := client.RbacV1().ClusterRoles().Create(ctx, cr, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, nil, fmt.Errorf("creating impersonation ClusterRole (need permission to grant impersonate): %w", err)
	}

	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Labels: labels},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: saName},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: saName, Namespace: namespace}},
	}
	if _, err := client.RbacV1().ClusterRoleBindings().Create(ctx, crb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, nil, fmt.Errorf("creating impersonation ClusterRoleBinding: %w", err)
	}

	ttl := int64(tokenTTL.Seconds())
	tr, err := client.CoreV1().ServiceAccounts(namespace).CreateToken(ctx, saName,
		&authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &ttl}}, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("minting tunnel token: %w", err)
	}

	authInfo := &clientcmdapi.AuthInfo{
		Token:                tr.Status.Token,
		Impersonate:          me.Username,
		ImpersonateGroups:    me.Groups,
		ImpersonateUserExtra: extraToStringMap(me.Extra),
	}

	cleanup := func() {
		// Best-effort teardown on a fresh context in case the parent's is done.
		bg := context.Background()
		_ = client.RbacV1().ClusterRoleBindings().Delete(bg, saName, metav1.DeleteOptions{})
		_ = client.RbacV1().ClusterRoles().Delete(bg, saName, metav1.DeleteOptions{})
		_ = client.CoreV1().ServiceAccounts(namespace).Delete(bg, saName, metav1.DeleteOptions{})
	}
	return authInfo, cleanup, nil
}

// impersonationRules grants impersonate on exactly the caller's user, groups,
// and extras — least privilege, so the token can only become this one identity.
func impersonationRules(me authnv1.UserInfo) []rbacv1.PolicyRule {
	rules := []rbacv1.PolicyRule{
		{APIGroups: []string{""}, Resources: []string{"users"}, Verbs: []string{"impersonate"}, ResourceNames: []string{me.Username}},
	}
	if len(me.Groups) > 0 {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{""}, Resources: []string{"groups"}, Verbs: []string{"impersonate"}, ResourceNames: me.Groups,
		})
	}
	for key, values := range me.Extra {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups:     []string{"authentication.k8s.io"},
			Resources:     []string{"userextras/" + key},
			Verbs:         []string{"impersonate"},
			ResourceNames: []string(values),
		})
	}
	return rules
}

// extraToStringMap converts SelfSubjectReview extras to the kubeconfig shape.
func extraToStringMap(extra map[string]authnv1.ExtraValue) map[string][]string {
	if len(extra) == 0 {
		return nil
	}
	out := make(map[string][]string, len(extra))
	for k, v := range extra {
		out[k] = []string(v)
	}
	return out
}

// serviceAccountName derives a DNS-1123 ServiceAccount name from the tunnel
// hostname's first label, so the SA is traceable to its tunnel.
func serviceAccountName(publicURL string) string {
	host := publicURL
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	label := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		label = host[:i]
	}
	name := "kubectl-add-tunnel-" + label
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	return name
}
