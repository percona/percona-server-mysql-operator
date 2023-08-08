package ps

import (
	"context"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

const controllerRevisionHash = "controller-revision-hash"

func (r *PerconaServerMySQLReconciler) smartUpdate(ctx context.Context, sts *appsv1.StatefulSet, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	if cr.Spec.Pause {
		return nil
	}

	currentSet := sts
	err := r.Client.Get(ctx, types.NamespacedName{
		Name:      sts.Name,
		Namespace: sts.Namespace,
	}, currentSet)
	if err != nil {
		return errors.Wrap(err, "failed to get current sfs")
	}

	pods := corev1.PodList{}
	if err := r.Client.List(ctx, &pods, &client.ListOptions{
		Namespace:     currentSet.Namespace,
		LabelSelector: labels.SelectorFromSet(currentSet.Labels),
	}); err != nil {
		return errors.Wrap(err, "get pod list")
	}

	if !stsChanged(currentSet, pods.Items) {
		return nil
	}

	log.Info("statefulSet was changed, run smart update")

	if cr.HAProxyEnabled() && cr.Status.HAProxy.State != apiv1alpha1.StateReady {
		log.Info("Waiting for HAProxy to be ready before smart update")
		return nil
	}

	if cr.RouterEnabled() && cr.Status.Router.State != apiv1alpha1.StateReady {
		log.Info("Waiting for MySQL Router to be ready before smart update")
		return nil
	}

	running, err := r.isBackupRunning(ctx, cr)
	if err != nil {
		log.Error(err, "can't start 'SmartUpdate'")
		return nil
	}
	if running {
		log.Info("can't start/continue 'SmartUpdate': backup is running")
		return nil
	}

	if currentSet.Status.ReadyReplicas < currentSet.Status.Replicas {
		log.Info("Can't start/continue 'SmartUpdate': waiting for all replicas are ready")
		return nil
	}

	primaryHost, err := r.getPrimaryHost(ctx, cr)
	if err != nil {
		return err
	}
	idx, err := getPodIndexFromHostname(primaryHost)
	if err != nil {
		return err
	}
	primPod, err := getMySQLPod(ctx, r.Client, cr, idx)
	if err != nil {
		return errors.Wrap(err, "get primary pod")
	}
	log.Info("primary pod", "name", primPod.Name)

	for _, pod := range pods.Items {
		pod := pod
		if pod.Name == primPod.Name {
			continue
		}

		log.Info("apply changes to secondary pod", "podName", pod.Name)

		if pod.ObjectMeta.Labels[controllerRevisionHash] == sts.Status.UpdateRevision {
			log.Info("pod updated updated", "podName", pod.Name)
			continue
		}

		err = r.Client.Delete(ctx, &pod)
		// We need to wait to allow sts.Status.ReadyReplicas to be updated after pod deletion
		time.Sleep(5 * time.Second)
		return err
	}

	log.Info("apply changes to primary pod", "pod name", primPod.Name)

	if primPod.ObjectMeta.Labels[controllerRevisionHash] == sts.Status.UpdateRevision {
		log.Info("primary pod updated updated", "primPod name", primPod.Name)
		log.Info("smart update finished")
		return nil
	}

	err = r.Client.Delete(ctx, primPod)
	// We need to wait to allow sts.Status.ReadyReplicas to be updated after pod deletion
	time.Sleep(5 * time.Second)
	return err
}

func stsChanged(sts *appsv1.StatefulSet, pods []corev1.Pod) bool {
	// When https://github.com/kubernetes/kubernetes/issues/73492 bug gets fixed,
	// we can simply compare sts.Status.UpdateRevision with sts.Status.CurrentRevision
	for _, pod := range pods {
		if pod.ObjectMeta.Labels["controller-revision-hash"] != sts.Status.UpdateRevision {
			return true

		}
	}

	return false
}

func (r *PerconaServerMySQLReconciler) isBackupRunning(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (bool, error) {
	bcpList := apiv1alpha1.PerconaServerMySQLBackupList{}
	if err := r.Client.List(ctx, &bcpList, &client.ListOptions{Namespace: cr.Namespace}); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "failed to get backup object")
	}

	for _, bcp := range bcpList.Items {
		if bcp.Spec.ClusterName != cr.Name {
			continue
		}

		if bcp.Status.State == apiv1alpha1.BackupRunning || bcp.Status.State == apiv1alpha1.BackupStarting {
			return true, nil
		}
	}

	return false, nil
}
