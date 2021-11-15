package k8s

import (
	"context"
	"io/ioutil"
	"os"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func Namespace() (string, error) {
	nsBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}

func operatorPod(ctx context.Context, rdr client.Reader) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	ns, err := Namespace()
	if err != nil {
		return nil, errors.Wrap(err, "get namespace")
	}

	nn := types.NamespacedName{Namespace: ns, Name: os.Getenv("HOSTNAME")}
	if err := rdr.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}

func InitImage(ctx context.Context, rdr client.Reader) (string, error) {
	pod, err := operatorPod(ctx, rdr)
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
