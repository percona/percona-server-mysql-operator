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

	// Picked by ordinal, not by the presence of a pod: a scale up must expand the
	// PVCs it is about to reuse before their replica starts cloning into them.
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

		size := pvcSize(pvc)

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

	// what an up to date volume claim template holds, unlike the rounded up size
	crRequest := requested.DeepCopy()

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

			if pvcSize(pvc).Cmp(requested) >= 0 {
				updatedPVCs++
				log.Info("PVC resize finished", "name", pvc.Name, "size", pvcSize(pvc))
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
				if lastSeen(event).Before(resizeStartedAt) {
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
			// Recreated only to update the immutable volume claim template. The
			// delete orphans the pods and the new set adopts them back.
			if configured.Cmp(crRequest) != 0 {
				log.Info("Deleting statefulset", "configured", configured, "requested", crRequest)

				if err := r.stashAppliedConfig(ctx, cr, sts); err != nil {
					return errors.Wrapf(err, "stash applied config of statefulset/%s", sts.Name)
				}

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

	// stamped once per resize, so the events belonging to it stay in the window
	if !cr.PVCResizeInProgress() {
		now := metav1.Now().Format(time.RFC3339)

		err = k8s.AnnotateObject(ctx, r.Client, cr, map[naming.AnnotationKey]string{naming.AnnotationPVCResizeInProgress: now})
		if err != nil {
			return errors.Wrap(err, "annotate ps")
		}
	}

	log.Info("Resizing PVCs", "requested", requested, "actual", actual, "pvcList", strings.Join(pvcsToUpdate, ","))

	for _, pvc := range pvcList.Items {
		if !slices.Contains(pvcsToUpdate, pvc.Name) {
			continue
		}

		if pvcSize(pvc).Cmp(requested) >= 0 {
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

// stashAppliedConfig copies the record of the configuration already applied to
// the running mysqld from the statefulset onto the cr, so that it survives the
// statefulset being deleted and rebuilt for a resize.
//
// The replacement set is built from the cr and comes back without the record.
// EnsureObjectWithHash preserves the annotation when it updates a set but not
// when it creates one, so without the copy the next configuration pass reads an
// empty record, treats every calculated variable as new, and re-applies the lot.
// The variables that cannot be set at runtime then restart the whole cluster
// over a resize that needs no restart.
func (r *PerconaServerMySQLReconciler) stashAppliedConfig(
	ctx context.Context,
	cr *psv1.PerconaServerMySQL,
	sts *appsv1.StatefulSet,
) error {
	applied, ok := sts.GetAnnotations()[naming.AnnotationLastAppliedConfig.String()]
	if !ok {
		return nil
	}

	return k8s.AnnotateObject(ctx, r.Client, cr, map[naming.AnnotationKey]string{
		naming.AnnotationLastAppliedConfig: applied,
	})
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

	// a statefulset only names claims after a non negative ordinal in its
	// shortest form, and any other claim it can never reuse
	if ordinal < 0 || strconv.Itoa(ordinal) != suffix {
		return 0, false
	}

	return ordinal, true
}

// filesystemResizePending reports that the volume is expanded and only its
// filesystem is still to be grown. Unlike nodeResizePending this sticks around
// until the volume is mounted, so it can outlive the resize that set it.
func filesystemResizePending(pvc corev1.PersistentVolumeClaim) bool {
	return slices.ContainsFunc(pvc.Status.Conditions, func(c corev1.PersistentVolumeClaimCondition) bool {
		return c.Type == corev1.PersistentVolumeClaimFileSystemResizePending && c.Status == corev1.ConditionTrue
	})
}

// lastSeen reports when the event was last seen. Repeated events are coalesced
// into one object that keeps its first timestamp and only moves the last one, so
// the first timestamp can predate the resize that made the event recur.
func lastSeen(event eventsv1.Event) time.Time {
	times := []time.Time{
		event.EventTime.Time,
		event.DeprecatedLastTimestamp.Time,
		event.DeprecatedFirstTimestamp.Time,
		event.CreationTimestamp.Time,
	}
	if event.Series != nil {
		times = append(times, event.Series.LastObservedTime.Time)
	}

	var latest time.Time
	for _, t := range times {
		if t.After(latest) {
			latest = t
		}
	}

	return latest
}

// pvcSize reports the size of the volume behind the PVC. It deliberately does
// not look at pods: a claim keeps reporting its old capacity until kubelet grows
// its filesystem on mount, and whether a pod object exists says nothing about
// whether that has happened yet.
//
// The allocated size is used rather than the requested one because a new request
// lands in the spec at once, while allocatedResources only follows when the
// resize controller picks it up.
func pvcSize(pvc corev1.PersistentVolumeClaim) *resource.Quantity {
	// An unbound claim has no volume to expand: it is created at the size its
	// spec asks for, so that is the size it is going to have.
	if pvc.Status.Capacity.Storage().IsZero() {
		return pvc.Spec.Resources.Requests.Storage()
	}

	// the volume is expanded and only its filesystem is still to be grown
	if nodeResizePending(pvc) {
		return pvc.Status.AllocatedResources.Storage()
	}

	// Where nothing reports the per request status this condition is all there
	// is. It can be left over from an earlier expansion, so the volume may turn
	// out to be smaller than asked for, which the next resize picks up once a
	// replica mounts it and its capacity is known again. Ignoring it instead
	// leaves the claim below the request for good, which holds the replicas back.
	if !reportsResizeStatus(pvc) && filesystemResizePending(pvc) {
		return pvc.Spec.Resources.Requests.Storage()
	}

	return pvc.Status.Capacity.Storage()
}

// nodeResizePending reports whether the volume is expanded up to its allocated
// size and only its filesystem is still to be grown. Kept per resize request, so
// it cannot outlive the resize that set it.
func nodeResizePending(pvc corev1.PersistentVolumeClaim) bool {
	return pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage] == corev1.PersistentVolumeClaimNodeResizePending
}

// reportsResizeStatus reports whether the cluster fills in the per request resize
// status at all. It needs RecoverVolumeExpansionFailure, which is not enabled on
// every supported platform.
func reportsResizeStatus(pvc corev1.PersistentVolumeClaim) bool {
	_, ok := pvc.Status.AllocatedResourceStatuses[corev1.ResourceStorage]
	return ok
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
