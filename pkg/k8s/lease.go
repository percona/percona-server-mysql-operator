package k8s

import (
	"context"
	"time"

	"github.com/pkg/errors"
	coordv1 "k8s.io/api/coordination/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

const restoreLeaseDurationSeconds int32 = 30

func restoreLeaseName(clusterName string) string {
	return "restore-lock-" + clusterName
}

func AcquireRestoreLease(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQLRestore) (*coordv1.Lease, error) {
	var result *coordv1.Lease
	nn := types.NamespacedName{
		Name:      restoreLeaseName(cr.Spec.ClusterName),
		Namespace: cr.Namespace,
	}

	err := k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		now := time.Now()
		lease, err := GetRestoreLease(ctx, cl, cr.Namespace, cr.Spec.ClusterName)
		if err != nil {
			return err
		}
		if lease == nil {
			lease = &coordv1.Lease{
				ObjectMeta: metav1.ObjectMeta{
					Name:      nn.Name,
					Namespace: nn.Namespace,
				},
				Spec: coordv1.LeaseSpec{
					HolderIdentity:       &cr.Name,
					LeaseDurationSeconds: ptr.To(restoreLeaseDurationSeconds),
					AcquireTime:          &metav1.MicroTime{Time: now},
					RenewTime:            &metav1.MicroTime{Time: now},
				},
			}
			if err := cl.Create(ctx, lease); err != nil {
				return errors.Wrap(err, "create lease")
			}

			result = lease.DeepCopy()
			return nil
		}

		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != cr.Name && IsLeaseActive(lease) {
			result = lease.DeepCopy()
			return nil
		}

		if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != cr.Name || lease.Spec.AcquireTime == nil {
			lease.Spec.AcquireTime = &metav1.MicroTime{Time: now}
		}
		lease.Spec.HolderIdentity = &cr.Name
		lease.Spec.LeaseDurationSeconds = ptr.To(restoreLeaseDurationSeconds)
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now}

		if err := cl.Update(ctx, lease); err != nil {
			return err
		}
		result = lease.DeepCopy()

		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "create or update lease")
	}

	return result, nil
}

func ReleaseRestoreLease(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQLRestore) error {
	switch cr.Status.State {
	case apiv1.RestoreNew, apiv1.RestoreStarting, apiv1.RestoreRunning:
		return nil
	}
	lease, err := GetRestoreLease(ctx, cl, cr.Namespace, cr.Spec.ClusterName)
	if err != nil {
		return errors.Wrap(err, "get restore lease")
	}

	if lease == nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != cr.Name {
		return nil
	}

	if err := cl.Delete(ctx, lease); client.IgnoreNotFound(err) != nil {
		return errors.Wrap(err, "delete lease")
	}
	return nil
}

func GetRestoreLease(ctx context.Context, cl client.Client, namespace, clusterName string) (*coordv1.Lease, error) {
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      restoreLeaseName(clusterName),
			Namespace: namespace,
		},
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(lease), lease); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, errors.Wrap(err, "get lease")
	}

	return lease, nil
}

func IsLeaseActive(lease *coordv1.Lease) bool {
	if lease == nil || lease.Spec.HolderIdentity == nil || lease.Spec.LeaseDurationSeconds == nil {
		return false
	}

	lastRenew := lease.Spec.RenewTime
	if lastRenew == nil {
		return false
	}

	expiry := lastRenew.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return time.Now().Before(expiry)
}
