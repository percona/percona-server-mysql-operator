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

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
)

func Namespace() (string, error) {
	nsBytes, err := ioutil.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(nsBytes)), nil
}

func operatorPod(cl client.Client) (corev1.Pod, error) {
	operatorPod := corev1.Pod{}

	ns, err := Namespace()
	if err != nil {
		return operatorPod, errors.Wrap(err, "get namespace")
	}

	if err := cl.Get(context.TODO(), types.NamespacedName{
		Namespace: ns,
		Name:      os.Getenv("HOSTNAME"),
	}, &operatorPod); err != nil {
		return operatorPod, err
	}

	return operatorPod, nil
}

func InitImage(cl client.Client, cr *v2.PerconaServerForMySQL) (string, error) {
	pod, err := operatorPod(cl)
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
