package mysql

import (
	"context"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetReadyMySQLPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL) (*corev1.Pod, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, MatchLabels(cr), cr.Namespace)
	if err != nil {
		return nil, errors.Wrap(err, "get pods")
	}

	for i, pod := range pods {
		if k8s.IsPodReady(pod) {
			return &pods[i], nil
		}
	}
	return nil, errors.New("no ready pods")
}

func GetMySQLPod(ctx context.Context, cl client.Reader, cr *apiv1alpha1.PerconaServerMySQL, idx int) (*corev1.Pod, error) {
	pod := &corev1.Pod{}

	nn := types.NamespacedName{Namespace: cr.Namespace, Name: PodName(cr, idx)}
	if err := cl.Get(ctx, nn, pod); err != nil {
		return nil, err
	}

	return pod, nil
}
