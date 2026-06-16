package ps

import (
	"context"
	"strings"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/metrics"
)

// reconcileStorageAutoscaling checks PVC disk usage and triggers resize if needed
func (r *PerconaServerMySQLReconciler) reconcileStorageAutoscaling(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	log := logf.FromContext(ctx).WithName("StorageAutoscaling")
	ctx = logf.IntoContext(ctx, log)

	autoscalingSpec := cr.Spec.StorageAutoscaling()
	if autoscalingSpec == nil || !autoscalingSpec.Enabled {
		return nil
	}

	if cr.Spec.StorageScaling != nil && cr.Spec.StorageScaling.VolumeExternalAutoscaling {
		return nil
	}

	if !cr.Spec.IsVolumeExpansionEnabled() {
		return nil
	}

	volumeSpec := cr.Spec.MySQL.VolumeSpec

	if volumeSpec == nil || volumeSpec.PersistentVolumeClaim == nil {
		return nil
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mysql.Name(cr),
			Namespace: cr.Namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil {
		if k8serrors.IsNotFound(err) {
			log.V(1).Info("skipping storage autoscaling: mysql statefulset not found yet")
			return nil
		}
		return errors.Wrap(err, "failed to get mysql sts")
	}

	if cr.PVCResizeInProgress() {
		log.V(1).Info("Skipping storage autoscaling: PVC resize already in progress")
		return nil
	}

	ls := mysql.MatchLabels(cr)
	pvcList := &corev1.PersistentVolumeClaimList{}
	err := r.List(ctx, pvcList, &client.ListOptions{
		Namespace:     cr.Namespace,
		LabelSelector: labels.SelectorFromSet(ls),
	})
	if err != nil {
		return errors.Wrap(err, "list PVCs for autoscaling")
	}

	podList := &corev1.PodList{}
	err = r.List(ctx, podList, &client.ListOptions{
		Namespace:     cr.Namespace,
		LabelSelector: labels.SelectorFromSet(ls),
	})
	if err != nil {
		return errors.Wrap(err, "list pods for autoscaling")
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if !validatePVCName(*pvc, sts.Name) {
			continue
		}

		podName := extractPodNameFromPVC(pvc.Name, sts.Name)
		pod := findPodByName(podList, podName)
		if pod == nil {
			continue
		}

		if err := r.checkAndResizePVC(ctx, cr, pvc, pod); err != nil {
			log.Error(err, "failed to check/resize PVC", "pvc", pvc.Name)
			r.updateAutoscalingStatus(ctx, cr, pvc.Name, nil, err)
		}
	}

	return nil
}

// checkAndResizePVC checks a single PVC and triggers resize if needed
func (r *PerconaServerMySQLReconciler) checkAndResizePVC(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pvc *corev1.PersistentVolumeClaim,
	pod *corev1.Pod,
) error {
	log := logf.FromContext(ctx).WithValues("pvc", pvc.Name)
	ctx = logf.IntoContext(ctx, log)

	if !k8s.IsPodReady(*pod) {
		log.V(1).Info("Skipping PVC metrics check: pod not ready", "pod", pod.Name)
		return nil
	}

	usage, err := metrics.GetPVCUsage(ctx, r.ClientCmd, pod, pvc.Name)
	if err != nil {
		return errors.Wrap(err, "get PVC usage from metrics")
	}

	r.updateAutoscalingStatus(ctx, cr, pvc.Name, usage, nil)

	if pvc.Status.Capacity == nil || pvc.Status.Capacity.Storage() == nil || pvc.Status.Capacity.Storage().IsZero() {
		return nil
	}

	if !r.shouldTriggerResize(ctx, cr, pvc, usage) {
		return nil
	}

	newSize := r.calculateNewSize(cr, pvc)

	log.Info("triggering storage autoscaling",
		"currentSize", pvc.Status.Capacity.Storage().String(),
		"newSize", newSize.String(),
		"usagePercent", usage.UsagePercent,
		"threshold", cr.Spec.StorageAutoscaling().TriggerThresholdPercent)

	return r.triggerResize(ctx, cr, pvc, newSize)
}

// shouldTriggerResize determines if a PVC should be resized
func (r *PerconaServerMySQLReconciler) shouldTriggerResize(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pvc *corev1.PersistentVolumeClaim,
	usage *metrics.PVCUsage,
) bool {
	log := logf.FromContext(ctx)
	config := cr.Spec.StorageAutoscaling()

	if usage.UsagePercent < config.TriggerThresholdPercent {
		return false
	}

	if !config.MaxSize.IsZero() {
		currentSize := pvc.Status.Capacity.Storage()
		if currentSize.Cmp(config.MaxSize) >= 0 {
			log.Info("PVC already at maxSize",
				"currentSize", currentSize.String(),
				"maxSize", config.MaxSize.String())
			return false
		}
	}

	for _, cond := range pvc.Status.Conditions {
		if (cond.Type == corev1.PersistentVolumeClaimResizing ||
			cond.Type == corev1.PersistentVolumeClaimFileSystemResizePending) &&
			cond.Status == corev1.ConditionTrue {
			log.V(1).Info("resize already in progress", "condition", cond.Type)
			return false
		}
	}

	return true
}

// calculateNewSize calculates the new PVC size based on current size and growth step
func (r *PerconaServerMySQLReconciler) calculateNewSize(
	cr *apiv1.PerconaServerMySQL,
	pvc *corev1.PersistentVolumeClaim,
) resource.Quantity {
	config := cr.Spec.StorageAutoscaling()
	currentSize := pvc.Status.Capacity.Storage()

	newSizeBytes := currentSize.Value() + config.GrowthStep.Value()
	newSize := *resource.NewQuantity(newSizeBytes, resource.BinarySI)

	if !config.MaxSize.IsZero() && newSize.Cmp(config.MaxSize) > 0 {
		newSize = config.MaxSize
	}

	return newSize
}

// triggerResize updates the CR volumeSpec to trigger a resize operation
func (r *PerconaServerMySQLReconciler) triggerResize(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pvc *corev1.PersistentVolumeClaim,
	newSize resource.Quantity,
) error {
	log := logf.FromContext(ctx)

	orig := cr.DeepCopy()

	cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = newSize

	if err := r.Patch(ctx, cr.DeepCopy(), client.MergeFrom(orig)); err != nil {
		return errors.Wrap(err, "patch CR with new storage size")
	}

	log.Info("storage autoscaling initiated",
		"oldSize", pvc.Status.Capacity.Storage().String(),
		"newSize", newSize.String())

	r.Recorder.Eventf(cr, corev1.EventTypeNormal, "StorageAutoscalingTriggered", "Storage autoscaling triggered for PVC %s", pvc.Name)

	return nil
}

// updateAutoscalingStatus updates the status for a specific PVC
func (r *PerconaServerMySQLReconciler) updateAutoscalingStatus(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	pvcName string,
	usage *metrics.PVCUsage,
	usageErr error,
) {
	log := logf.FromContext(ctx)

	if pvcName == "" {
		return
	}

	err := writeStatus(ctx, r.Client, client.ObjectKeyFromObject(cr), func(status *apiv1.PerconaServerMySQLStatus) error {
		if status.StorageAutoscaling == nil {
			status.StorageAutoscaling = make(map[string]apiv1.StorageAutoscalingStatus)
		}

		pvcStatus := status.StorageAutoscaling[pvcName]

		if usage != nil {
			newSize := resource.NewQuantity(usage.TotalBytes, resource.BinarySI)
			if pvcStatus.CurrentSize != "" {
				oldSize, parseErr := resource.ParseQuantity(pvcStatus.CurrentSize)
				if parseErr == nil && newSize.Cmp(oldSize) > 0 {
					pvcStatus.LastResizeTime = metav1.Time{Time: time.Now()}
					pvcStatus.ResizeCount++
				}
			}
			pvcStatus.CurrentSize = newSize.String()
			pvcStatus.LastError = ""
		}

		if usageErr != nil {
			pvcStatus.LastError = usageErr.Error()
		}

		status.StorageAutoscaling[pvcName] = pvcStatus
		return nil
	})
	if err != nil {
		log.Error(err, "failed to update autoscaling status", "pvc", pvcName)
	}
}

// extractPodNameFromPVC extracts the pod name from a datadir PVC name.
// PVC format: "datadir-<statefulset-name>-<index>"
// Pod format: "<statefulset-name>-<index>"
func extractPodNameFromPVC(pvcName string, stsName string) string {
	prefix := mysql.DataVolumeName + "-" + stsName + "-"
	if suffix, ok := strings.CutPrefix(pvcName, prefix); ok {
		return stsName + "-" + suffix
	}
	return ""
}

// findPodByName finds a pod in a list by name
func findPodByName(podList *corev1.PodList, podName string) *corev1.Pod {
	for i := range podList.Items {
		if podList.Items[i].Name == podName {
			return &podList.Items[i]
		}
	}
	return nil
}
