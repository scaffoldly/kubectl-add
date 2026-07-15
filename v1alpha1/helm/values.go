package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// valuesKey is the ConfigMap key holding the persisted values.yaml.
const valuesKey = "values.yaml"

// ValuesName is the ConfigMap name that persists the values for a chart
// URL: stable per URL, so a later add of the same chart reuses it.
func ValuesName(resource string) string {
	sum := sha256.Sum256([]byte(resource))
	return "kubectl-add-values-" + hex.EncodeToString(sum[:])[:8]
}

// LoadValues returns the persisted values for name, reporting whether the
// ConfigMap exists.
func LoadValues(ctx context.Context, client kubernetes.Interface, namespace, name string) ([]byte, bool, error) {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("helm: loading values %s: %w", name, err)
	}
	return []byte(cm.Data[valuesKey]), true, nil
}

// StoreValues persists values into the named ConfigMap, creating it or
// updating in place.
func StoreValues(ctx context.Context, client kubernetes.Interface, namespace, name string, values []byte) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string]string{valuesKey: string(values)},
	}
	_, err := client.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = client.CoreV1().ConfigMaps(namespace).Update(ctx, cm, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("helm: storing values %s: %w", name, err)
	}
	return nil
}
