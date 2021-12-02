package k8s

import (
	"context"
	"os"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func InitImage(ctx context.Context, cl client.Reader) (string, error) {
	pod, err := operatorPod(ctx, cl)
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

func operatorPod(ctx context.Context, cl client.Reader) (*corev1.Pod, error) {
	ns, err := DefaultAPINamespace()
	if err != nil {
		return nil, errors.Wrap(err, "get namespace")
	}

	pod := &corev1.Pod{}
	nn := types.NamespacedName{
		Namespace: ns,
		Name:      os.Getenv("HOSTNAME"),
	}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}
