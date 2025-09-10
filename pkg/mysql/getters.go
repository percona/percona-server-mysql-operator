package mysql

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

var (
	ErrNoReadyPods = errors.New("no ready pods")
)

func GetReadyPod(ctx context.Context, cl client.Reader, cr *apiv1.PerconaServerMySQL) (*corev1.Pod, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, MatchLabels(cr), cr.Namespace)
	if err != nil {
		return nil, errors.Wrap(err, "get pods")
	}

	for i, pod := range pods {
		if k8s.IsPodReady(pod) {
			return &pods[i], nil
		}
	}
	return nil, ErrNoReadyPods
}

func GetPod(ctx context.Context, cl client.Reader, cr *apiv1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}
