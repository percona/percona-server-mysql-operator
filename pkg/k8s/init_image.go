package k8s

import (
	"context"
	"io/ioutil"
	"os"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func InitImage(ctx context.Context, get APIGetter) (string, error) {
	pod, err := operatorPod(ctx, get)
	if err != nil {
		return "", errors.Wrap(err, "get operator pod")
	}

	for _, container := range pod.Spec.Containers {
		if container.Name == "manager" {
			return container.Image, nil
		}
	}

	return "", errors.New("manager container not found")
}

func operatorPod(ctx context.Context, get APIGetter) (*corev1.Pod, error) {
	ns, err := DefaultAPINamespace()
	if err != nil {
		return nil, errors.Wrap(err, "get namespace")
	}

	pod := &corev1.Pod{}
	nn := types.NamespacedName{
		Namespace: ns,
		Name:      os.Getenv("HOSTNAME"),
	}
	if err := get.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}

func DefaultAPINamespace() (string, error) {
	nsBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}
