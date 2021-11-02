package k8s

import (
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const WatchNamespaceEnvVar = "WATCH_NAMESPACE"

// Probe is a k8s helper to create Probe object
func Probe(pb *corev1.Probe, cmd ...string) *corev1.Probe {
	pb.Exec = &corev1.ExecAction{
		Command: cmd,
	}
	return pb
}

// SecretKeySelector is a k8s helper to create SecretKeySelector object
func SecretKeySelector(name, key string) *corev1.SecretKeySelector {
	evs := &corev1.SecretKeySelector{}
	evs.Name = name
	evs.Key = key

	return evs
}

// GetWatchNamespace returns the namespace the operator should be watching for changes
func GetWatchNamespace() (string, error) {
	ns, found := os.LookupEnv(WatchNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", WatchNamespaceEnvVar)
	}
	return ns, nil
}

func compareMaps(x, y map[string]string) bool {
	if len(x) != len(y) {
		return false
	}

	for k, v := range x {
		yVal, ok := y[k]
		if !ok || yVal != v {
			return false
		}
	}

	return true
}

func IsObjectMetaEqual(old, new metav1.Object) bool {
	return compareMaps(old.GetAnnotations(), new.GetAnnotations()) &&
		compareMaps(old.GetLabels(), new.GetLabels())
}

func IsLabelsEqual(old, new map[string]string) bool {
	return compareMaps(old, new)
}

// CloneLabels returns clone of the provided labels.
func CloneLabels(src map[string]string) map[string]string {
	clone := make(map[string]string)
	for k, v := range src {
		clone[k] = v
	}
	return clone
}

func RemoveLabel(obj client.Object, key string) {
	delete(obj.GetLabels(), key)
}

func AddLabel(obj client.Object, key, value string) {
	labels := obj.GetLabels()
	labels[key] = value
	obj.SetLabels(labels)
}
