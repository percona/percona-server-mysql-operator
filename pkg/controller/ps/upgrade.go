package ps

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

func (r *PerconaServerMySQLReconciler) smartUpdate(ctx context.Context, sts *appsv1.StatefulSet, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx)

	if cr.Spec.Pause {
		return nil
	}

	if cr.HAProxyEnabled() && cr.Status.HAProxy.State != apiv1alpha1.StateReady {
		log.Info("Waiting for HAProxy to be ready before smart update")
		return nil
	}

	// TODO: check Router like HA proxy is checked

	// sleep to get new sfs revision
	time.Sleep(time.Second)

	currentSet := sts
	err := r.Client.Get(context.TODO(), types.NamespacedName{
		Name:      sts.Name,
		Namespace: sts.Namespace,
	}, currentSet)
	if err != nil {
		return errors.Wrap(err, "failed to get current sfs")
	}

	list := corev1.PodList{}
	if err := r.Client.List(context.TODO(),
		&list,
		&client.ListOptions{
			Namespace:     sts.Namespace,
			LabelSelector: labels.SelectorFromSet(sts.Labels),
		},
	); err != nil {
		return errors.Wrap(err, "get pod list")
	}
	statefulSetChanged := false
	for _, pod := range list.Items {
		if pod.ObjectMeta.Labels["controller-revision-hash"] != sts.Status.UpdateRevision {
			statefulSetChanged = true
			break
		}
	}
	if !statefulSetChanged {
		return nil
	}

	log.Info("statefulSet was changed, run smart update")

	// TODO: check if the backup is running

	running, err := r.isBackupRunning(ctx, cr)
	if err != nil {
		log.Error(err, "can't start 'SmartUpdate'")
		return nil
	}
	if running {
		log.Info("can't start/continue 'SmartUpdate': backup is running")
		return nil
	}

	if sts.Status.ReadyReplicas < sts.Status.Replicas {
		log.Info("can't start/continue 'SmartUpdate': waiting for all replicas are ready")
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

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name > list.Items[j].Name
	})

	waitLimit := cr.Spec.MySQL.LivenessProbe.InitialDelaySeconds
	for _, pod := range list.Items {
		pod := pod
		if pod.Name == primPod.Name {
			continue
		} else {
			log.Info("apply changes to secondary pod", "pod name", pod.Name)
			if err := r.applyNWait(ctx, cr, sts, &pod, waitLimit); err != nil {
				return errors.Wrap(err, "failed to apply changes")
			}
		}
	}

	log.Info("apply changes to primary pod", "pod name", primPod.Name)
	if err := r.applyNWait(ctx, cr, sts, primPod, waitLimit); err != nil {
		return errors.Wrap(err, "failed to apply changes")
	}

	log.Info("smart update finished")

	return nil
}

func (r *PerconaServerMySQLReconciler) isBackupRunning(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) (bool, error) {
	bcpList := v1alpha1.PerconaServerMySQLBackupList{}
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

		if bcp.Status.State == v1alpha1.BackupRunning || bcp.Status.State == v1alpha1.BackupStarting {
			return true, nil
		}
	}

	return false, nil
}

func (r *PerconaServerMySQLReconciler) applyNWait(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, sfs *appsv1.StatefulSet, pod *corev1.Pod, waitLimit int32) error {
	log := logf.FromContext(ctx)

	if pod.ObjectMeta.Labels["controller-revision-hash"] == sfs.Status.UpdateRevision {
		log.Info("pod already updated", "pod name", pod.Name)
	} else {
		if err := r.Client.Delete(ctx, pod); err != nil {
			return errors.Wrap(err, "failed to delete pod")
		}
	}

	orderInSts, err := getPodOrderInSts(sfs.Name, pod.Name)
	if err != nil {
		return errors.Errorf("compute pod order err, sfs name: %s, pod name: %s", sfs.Name, pod.Name)
	}
	if int32(orderInSts) >= *sfs.Spec.Replicas {
		log.Info("sfs scaled down, pod will not be started", "sfs", sfs.Name, "pod", pod.Name)
		return nil
	}

	if err := r.waitPodRestart(ctx, cr, sfs.Status.UpdateRevision, pod, waitLimit); err != nil {
		return errors.Wrap(err, "failed to wait pod")
	}

	// TODO: implement is pod online check
	// if err := r.waitUntilOnline(ctx, cr, sfs.Name, pod, waitLimit); err != nil {
	// 	return errors.Wrap(err, "failed to wait pxc status")
	// }

	return nil
}

func (r *PerconaServerMySQLReconciler) waitPodRestart(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, updateRevision string, pod *corev1.Pod, waitLimit int32) error {
	return retry(time.Second*10, time.Duration(waitLimit)*time.Second,
		func() (bool, error) {
			err := r.Client.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)
			if err != nil && !k8serrors.IsNotFound(err) {
				return false, errors.Wrap(err, "fetch pod")
			}

			// We update status in every loop to not wait until the end of smart update
			if err := r.reconcileCRStatus(ctx, cr); err != nil {
				return false, errors.Wrap(err, "reconcile status")
			}

			ready := false
			for _, container := range pod.Status.ContainerStatuses {
				if container.Name == "pxc" {
					ready = container.Ready

					if container.State.Waiting != nil {
						switch container.State.Waiting.Reason {
						case "ImagePullBackOff", "ErrImagePull", "CrashLoopBackOff":
							return false, errors.Errorf("pod %s is in %s state", pod.Name, container.State.Waiting.Reason)
						default:
							logf.FromContext(ctx).Info("pod is waiting", "pod name", pod.Name, "reason", container.State.Waiting.Reason)
						}
					}
				}
			}

			if pod.Status.Phase == corev1.PodFailed {
				return false, errors.Errorf("pod %s is in failed phase", pod.Name)
			}

			if pod.Status.Phase == corev1.PodRunning && pod.ObjectMeta.Labels["controller-revision-hash"] == updateRevision && ready {
				logf.FromContext(ctx).Info("pod is running", "pod name", pod.Name)
				return true, nil
			}

			return false, nil
		})
}

func getPodOrderInSts(stsName string, podName string) (int, error) {
	return strconv.Atoi(podName[len(stsName)+1:])
}

// retry runs func "f" every "in" time until "limit" is reached
// it also doesn't have an extra tail wait after the limit is reached
// and f func runs first time instantly
func retry(in, limit time.Duration, f func() (bool, error)) error {
	fdone, err := f()
	if err != nil {
		return err
	}
	if fdone {
		return nil
	}

	done := time.NewTimer(limit)
	defer done.Stop()
	tk := time.NewTicker(in)
	defer tk.Stop()

	for {
		select {
		case <-done.C:
			return errors.New("reach pod wait limit")
		case <-tk.C:
			fdone, err := f()
			if err != nil {
				return err
			}
			if fdone {
				return nil
			}
		}
	}
}
