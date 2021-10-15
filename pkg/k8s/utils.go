package k8s

import (
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
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
