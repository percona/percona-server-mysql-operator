package mysql

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
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

var ErrRolloutInProgress = errors.New("rollout in progress")

// GetAppliedCRVersion returns the version of the CR that is currently applied to the StatefulSet.
// It returns the current CR_VERSION env variable value from the StatefulSet container spec.
// If the StatefulSet has not yet rolled out, it returns ErrRolloutInProgress.
func GetAppliedCRVersion(ctx context.Context, cl client.Reader, cr *apiv1.PerconaServerMySQL) (string, error) {
	sfs := &appsv1.StatefulSet{}
	if err := cl.Get(ctx, NamespacedName(cr), sfs); err != nil {
		return "", err
	}

	rolledOut, err := rolloutComplete(ctx, cl, cr, sfs)
	if err != nil {
		return "", errors.Wrap(err, "check rollout")
	}
	if !rolledOut {
		return "", ErrRolloutInProgress
	}

	for _, c := range sfs.Spec.Template.Spec.Containers {
		if c.Name != AppName {
			continue
		}
		for _, env := range c.Env {
			if env.Name == crVersionEnvVar {
				return env.Value, nil
			}
		}
	}
	return "", nil
}

func rolloutComplete(ctx context.Context, cl client.Reader, cr *apiv1.PerconaServerMySQL, sfs *appsv1.StatefulSet) (bool, error) {
	if sfs.Status.ObservedGeneration != sfs.Generation {
		return false, nil
	}

	replicas := int32(1)
	if sfs.Spec.Replicas != nil {
		replicas = *sfs.Spec.Replicas
	}

	pods, err := k8s.PodsByLabels(ctx, cl, MatchLabels(cr), cr.Namespace)
	if err != nil {
		return false, errors.Wrap(err, "get pods")
	}
	if int32(len(pods)) != replicas {
		return false, nil
	}

	for _, pod := range pods {
		if pod.Labels[appsv1.StatefulSetRevisionLabel] != sfs.Status.UpdateRevision {
			return false, nil
		}
		if !k8s.IsPodReady(pod) {
			return false, nil
		}
	}

	return true, nil
}
