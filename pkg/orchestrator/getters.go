package orchestrator

import (
	"context"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetReadyPod(
	ctx context.Context,
	cl client.Client,
	cluster *apiv1.PerconaServerMySQL,
) (*corev1.Pod, error) {
	podList := &corev1.PodList{}
	err := cl.List(ctx, podList,
		&client.ListOptions{
			Namespace: cluster.Namespace,
			LabelSelector: labels.SelectorFromSet(map[string]string{
				naming.LabelInstance:  cluster.Name,
				naming.LabelComponent: naming.ComponentOrchestrator,
			}),
		},
	)
	if err != nil {
		return nil, errors.Wrap(err, "list orchestrator pods")
	}

	for _, pod := range podList.Items {
		if k8s.IsPodReady(pod) {
			return &pod, nil
		}
	}

	return nil, errors.New("no ready pods")
}
