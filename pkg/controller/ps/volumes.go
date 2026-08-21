package ps

import (
	"context"
	stderrors "errors"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	psv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	GiB = int64(1024 * 1024 * 1024)
)

func validatePVCName(pvc corev1.PersistentVolumeClaim, stsName string) bool {
	return strings.HasPrefix(pvc.Name, "datadir-"+stsName)
}

func reconcilePVCMetadata(ctx context.Context, cl client.Client, cr *psv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcilePVCMetadata")

	list := new(corev1.PersistentVolumeClaimList)
	if err := cl.List(ctx, list, &client.ListOptions{
		Namespace:     cr.Namespace,
		LabelSelector: labels.SelectorFromSet(mysql.MatchLabels(cr)),
	}); err != nil {
		return errors.Wrap(err, "list PVCs")
	}

	for _, pvc := range list.Items {
		kubernetesAnnotations := pvc.DeepCopy().Annotations
		maps.DeleteFunc(kubernetesAnnotations, func(k, v string) bool {
			keepKeys := []string{
				"pv.kubernetes.io/bind-completed",
				"pv.kubernetes.io/bound-by-controller",
				"volume.beta.kubernetes.io/storage-provisioner",
				"volume.kubernetes.io/selected-node",
				"volume.kubernetes.io/storage-provisioner",
			}
			return !slices.Contains(keepKeys, k)
		})

		expectedAnnotations := util.SSMapMerge(cr.GlobalAnnotations(), kubernetesAnnotations)
		expectedLabels := mysql.Labels(cr)

		if maps.Equal(expectedLabels, pvc.Labels) && maps.Equal(expectedAnnotations, pvc.Annotations) {
			continue
		}

		log.V(1).Info("Updating metadata for pvc", "pvc", pvc.Name)
		pvc.SetAnnotations(expectedAnnotations)
		pvc.SetLabels(expectedLabels)
		if err := cl.Update(ctx, &pvc); err != nil {
			return errors.Wrap(err, "failed to update pvc")
		}
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) reconcilePersistentVolumes(ctx context.Context, cr *psv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("PVCResize")

	if err := reconcilePVCMetadata(ctx, r.Client, cr); err != nil {
		return errors.Wrap(err, "failed to reconcile pvc metadata")
	}

	if cr.Spec.StorageScaling != nil && cr.Spec.StorageScaling.VolumeExternalAutoscaling {
		log.V(1).Info("skipping volume autoscaling: external autoscaling is enabled")
		return nil
	}

	ls := mysql.MatchLabels(cr)
	stsName := mysql.Name(cr)

	pvcList := &corev1.PersistentVolumeClaimList{}
	err := r.List(ctx, pvcList, &client.ListOptions{
		Namespace:     cr.Namespace,
		LabelSelector: labels.SelectorFromSet(ls),
	})
	if err != nil {
		return errors.Wrap(err, "list PVCs")
	}

	if len(pvcList.Items) == 0 {
		return nil
	}

	podList := corev1.PodList{}
	if err := r.List(ctx, &podList, client.InNamespace(cr.Namespace), client.MatchingLabels(ls)); err != nil {
		return errors.Wrap(err, "list pods")
	}

	podNames := make([]string, 0, len(podList.Items))
	for _, pod := range podList.Items {
		podNames = append(podNames, pod.Name)
	}

	// PVCs are picked by their ordinal rather than by the presence of their pod.
	// The ones a scale down left behind sit outside the requested size and can
	// never be resized, while the ones a scale up is about to reuse still have no
	// pod but must be resized before their replica starts cloning data into them.
	pvcsToUpdate := make([]string, 0, len(pvcList.Items))
	for _, pvc := range pvcList.Items {
		if !validatePVCName(pvc, stsName) {
			continue
		}

		ordinal, ok := pvcOrdinal(pvc.Name, stsName)
		if !ok || ordinal >= int(cr.Spec.MySQL.Size) {
			continue
		}

		pvcsToUpdate = append(pvcsToUpdate, pvc.Name)
	}

	if len(pvcsToUpdate) == 0 {
		return nil
	}

	var actual resource.Quantity
	for _, pvc := range pvcList.Items {
		if !slices.Contains(pvcsToUpdate, pvc.Name) {
			continue
		}

		if pvc.Status.Capacity == nil || pvc.Status.Capacity.Storage() == nil {
			continue
		}

		size := pvcSize(pvc, isPVCMounted(pvc, stsName, podNames))

		// we need to find the smallest size among all PVCs
		// since it indicates a failed resize operation
		if actual.IsZero() || size.Cmp(actual) < 0 {
			actual = *size
		}
	}

	if actual.IsZero() {
		return nil
	}

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: cr.Namespace,
		},
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(sts), sts); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "get statefulset %s", client.ObjectKeyFromObject(sts))
	}

	var volumeTemplate corev1.PersistentVolumeClaim
	for _, vct := range sts.Spec.VolumeClaimTemplates {
		if vct.Name == "datadir" {
			volumeTemplate = vct
		}
	}

	configured := volumeTemplate.Spec.Resources.Requests[corev1.ResourceStorage]
	requested := cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage]
	gib, err := RoundUpGiB(requested.Value())
	if err != nil {
		return errors.Wrap(err, "round GiB value")
	}

	requested = *resource.NewQuantity(gib*GiB, resource.BinarySI)

	if cr.PVCResizeInProgress() {
		resizeStartedAt, err := time.Parse(time.RFC3339, cr.GetAnnotations()[string(naming.AnnotationPVCResizeInProgress)])
		if err != nil {
			return errors.Wrap(err, "parse annotation")
		}

		updatedPVCs := 0
		var resizeErrors []error
		pendingResize := false
		for _, pvc := range pvcList.Items {
			if !slices.Contains(pvcsToUpdate, pvc.Name) {
				continue
			}

			mounted := isPVCMounted(pvc, stsName, podNames)

			if pvcSize(pvc, mounted).Cmp(requested) == 0 {
				updatedPVCs++
				if mounted {
					log.Info("PVC resize finished", "name", pvc.Name, "size", pvc.Status.Capacity.Storage())
				} else {
					log.Info("PVC expanded, filesystem will be resized once the replica starts", "name", pvc.Name, "requested", requested)
				}
				continue
			}

			for _, condition := range pvc.Status.Conditions {
				if condition.Status != corev1.ConditionTrue {
					continue
				}

				switch condition.Type {
				case corev1.PersistentVolumeClaimResizing, corev1.PersistentVolumeClaimFileSystemResizePending:
					log.V(1).Info(condition.Message, "pvc", pvc.Name, "type", condition.Type, "lastTransitionTime", condition.LastTransitionTime)
					log.Info("PVC resize in progress", "pvc", pvc.Name, "lastTransitionTime", condition.LastTransitionTime)
				}
			}

			events := &eventsv1.EventList{}
			if err := r.List(ctx, events, &client.ListOptions{
				Namespace:     sts.Namespace,
				FieldSelector: fields.SelectorFromSet(map[string]string{"regarding.name": pvc.Name}),
			}); err != nil {
				return errors.Wrapf(err, "list events for pvc/%s", pvc.Name)
			}

			for _, event := range events.Items {
				eventTime := event.EventTime.Time
				if event.EventTime.IsZero() {
					eventTime = event.DeprecatedFirstTimestamp.Time
				}

				if eventTime.Before(resizeStartedAt) {
					continue
				}

				switch event.Reason {
				case "Resizing", "ExternalExpanding", "FileSystemResizeRequired":
					log.Info("PVC resize in progress", "pvc", pvc.Name, "reason", event.Reason, "message", event.Note)
					pendingResize = true
				case "FileSystemResizeSuccessful":
					log.Info("PVC resize completed", "pvc", pvc.Name, "reason", event.Reason, "message", event.Note)
				case "VolumeResizeFailed", naming.EventExceededQuota, naming.EventStorageClassNotSupportResize:
					log.Error(nil, "PVC resize failed", "pvc", pvc.Name, "reason", event.Reason, "message", event.Note)

					resizeErrors = append(resizeErrors, errors.Errorf("%s pvc resize failed: %s: %s", pvc.Name, event.Reason, event.Note))
					continue
				}
			}
		}

		if len(resizeErrors) > 0 {
			if pendingResize {
				return nil
			}

			if err := r.handlePVCResizeFailure(ctx, cr, configured); err != nil {
				return err
			}
			return stderrors.Join(resizeErrors...)
		}

		resizeSucceeded := updatedPVCs == len(pvcsToUpdate)
		if resizeSucceeded {
			// The statefulset is only recreated to update its volume claim
			// template, which is immutable. When the template already asks for the
			// requested size there is nothing to update, and recreating it would
			// cost a rolling restart of the replicas for nothing.
			if configured.Cmp(requested) != 0 {
				log.Info("Deleting statefulset", "configured", configured, "requested", requested)

				if err := r.Delete(ctx, sts, client.PropagationPolicy("Orphan")); err != nil && !k8serrors.IsNotFound(err) {
					return errors.Wrapf(err, "delete statefulset/%s", sts.Name)
				}
			}

			if err := k8s.DeannotateObject(ctx, r.Client, cr, naming.AnnotationPVCResizeInProgress); err != nil {
				return errors.Wrap(err, "deannotate ps")
			}

			log.Info("PVC resize completed")

			return nil
		}

		log.Info("PVC resize in progress", "updated", updatedPVCs, "remaining", len(pvcsToUpdate)-updatedPVCs)
	}

	if requested.Cmp(actual) < 0 {
		if err := r.revertVolumeTemplate(ctx, cr, configured); err != nil {
			return errors.Wrapf(err, "revert volume template in ps/%s", cr.Name)
		}
		return errors.Errorf("requested storage (%s) is less than actual storage (%s)", requested.String(), actual.String())
	}

	if requested.Cmp(actual) == 0 {
		return nil
	}

	if !cr.Spec.IsVolumeExpansionEnabled() {
		// If expansion is disabled we should keep the old value
		cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = configured
		return nil
	}

	now := metav1.Now().Format(time.RFC3339)

	err = k8s.AnnotateObject(ctx, r.Client, cr, map[naming.AnnotationKey]string{naming.AnnotationPVCResizeInProgress: now})
	if err != nil {
		return errors.Wrap(err, "annotate ps")
	}

	log.Info("Resizing PVCs", "requested", requested, "actual", actual, "pvcList", strings.Join(pvcsToUpdate, ","))

	for _, pvc := range pvcList.Items {
		if !slices.Contains(pvcsToUpdate, pvc.Name) {
			continue
		}

		if pvcSize(pvc, isPVCMounted(pvc, stsName, podNames)).Cmp(requested) == 0 {
			log.Info("PVC already resized", "name", pvc.Name, "actual", pvc.Status.Capacity.Storage(), "requested", requested)
			continue
		}

		log.Info("Resizing PVC", "name", pvc.Name, "actual", pvc.Status.Capacity.Storage(), "requested", requested)

		err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
			p := new(corev1.PersistentVolumeClaim)
			if err := r.Get(ctx, client.ObjectKeyFromObject(&pvc), p); err != nil {
				return err
			}

			p.Spec.Resources.Requests[corev1.ResourceStorage] = requested

			return r.Update(ctx, p)
		})
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "exceeded quota"):
				r.Recorder.Event(&pvc, corev1.EventTypeWarning, naming.EventExceededQuota, "PVC resize failed")

				continue
			case strings.Contains(err.Error(), "the storageclass that provisions the pvc must support resize"):
				r.Recorder.Event(&pvc, corev1.EventTypeWarning, naming.EventStorageClassNotSupportResize, "PVC resize failed")

				continue
			default:
				return errors.Wrapf(err, "update persistentvolumeclaim/%s", pvc.Name)
			}
		}

		log.Info("PVC resize started", "pvc", pvc.Name, "requested", requested)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) handlePVCResizeFailure(ctx context.Context, cr *psv1.PerconaServerMySQL, originalSize resource.Quantity) error {
	if err := r.revertVolumeTemplate(ctx, cr, originalSize); err != nil {
		return errors.Wrapf(err, "revert volume template in ps/%s", cr.Name)
	}

	if err := k8s.DeannotateObject(ctx, r.Client, cr, naming.AnnotationPVCResizeInProgress); err != nil {
		return errors.Wrapf(err, "deannotate ps/%s", cr.Name)
	}

	return nil
}

func (r *PerconaServerMySQLReconciler) revertVolumeTemplate(ctx context.Context, cr *psv1.PerconaServerMySQL, originalSize resource.Quantity) error {
	log := logf.FromContext(ctx)

	orig := cr.DeepCopy()

	log.Info("Reverting volume template for PS", "originalSize", originalSize)
	cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim.Resources.Requests[corev1.ResourceStorage] = originalSize

	if err := r.Patch(ctx, cr.DeepCopy(), client.MergeFrom(orig)); err != nil {
		return errors.Wrapf(err, "patch ps/%s", cr.Name)
	}

	return nil
}

// pvcOrdinal returns the ordinal of the replica a datadir PVC belongs to.
func pvcOrdinal(pvcName, stsName string) (int, bool) {
	suffix, ok := strings.CutPrefix(pvcName, mysql.DataVolumeName+"-"+stsName+"-")
	if !ok {
		return 0, false
	}

	ordinal, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}

	return ordinal, true
}

// isPVCMounted reports whether the replica that mounts the PVC exists.
func isPVCMounted(pvc corev1.PersistentVolumeClaim, stsName string, podNames []string) bool {
	return slices.Contains(podNames, extractPodNameFromPVC(pvc.Name, stsName))
}

// pvcSize reports the size of the volume behind the PVC. A PVC that no replica
// mounts keeps reporting its old capacity until kubelet grows its filesystem, so
// once its volume is expanded the requested size is what it actually has.
func pvcSize(pvc corev1.PersistentVolumeClaim, mounted bool) *resource.Quantity {
	if !mounted && filesystemResizePending(pvc) {
		return pvc.Spec.Resources.Requests.Storage()
	}

	return pvc.Status.Capacity.Storage()
}

// filesystemResizePending reports whether the volume behind the PVC is already
// expanded and only its filesystem is still waiting to be grown.
func filesystemResizePending(pvc corev1.PersistentVolumeClaim) bool {
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == corev1.PersistentVolumeClaimFileSystemResizePending && condition.Status == corev1.ConditionTrue {
			return true
		}
	}

	return false
}

func roundUpSize(volumeSizeBytes int64, allocationUnitBytes int64) int64 {
	if allocationUnitBytes == 0 {
		return 0 // Avoid division by zero
	}
	return (volumeSizeBytes + allocationUnitBytes - 1) / allocationUnitBytes
}

// RoundUpGiB rounds up the volume size in bytes upto multiplications of GiB
// in the unit of GiB
func RoundUpGiB(volumeSizeBytes int64) (int64, error) {
	result := roundUpSize(volumeSizeBytes, GiB)
	if result > int64(math.MaxInt64) {
		return 0, errors.Errorf("rounded up size exceeds maximum value of int64: %d", result)
	}
	return result, nil
}
