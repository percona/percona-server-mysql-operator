package k8s

import (
	"context"
	"time"

	"github.com/pkg/errors"
	coordv1 "k8s.io/api/coordination/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const leaseDurationSeconds int32 = 30

var ErrLeaseAlreadyHeld = errors.New("lease held by another holder")

type IsHolderStaleFunc func(ctx context.Context, lease *coordv1.Lease) (bool, error)

func GetLease(ctx context.Context, client client.Client, leaseName, namespace string) (*coordv1.Lease, error) {
	lease := new(coordv1.Lease)
	if err := client.Get(ctx, types.NamespacedName{Name: leaseName, Namespace: namespace}, lease); err != nil {
		return nil, err
	}
	return lease, nil
}

func AcquireLease(ctx context.Context, cl client.Client, leaseName, holder, namespace string, checkStale IsHolderStaleFunc) error {
	lease := &coordv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      leaseName,
			Namespace: namespace,
		},
	}

	now := time.Now()
	_, err := controllerutil.CreateOrUpdate(ctx, cl, lease, func() error {
		if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != holder {
			if checkStale == nil {
				return ErrLeaseAlreadyHeld
			}

			stale, err := checkStale(ctx, lease)
			if err != nil {
				return errors.Wrap(err, "failed to check if lease holder is stale")
			}
			if !stale {
				return ErrLeaseAlreadyHeld
			}
		}

		if lease.Spec.AcquireTime == nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != holder {
			lease.Spec.HolderIdentity = &holder
			lease.Spec.AcquireTime = &metav1.MicroTime{Time: now}
		}
		lease.Spec.RenewTime = &metav1.MicroTime{Time: now}
		lease.Spec.LeaseDurationSeconds = ptr.To(leaseDurationSeconds)
		return nil
	})
	return err
}

func ReleaseLease(ctx context.Context, cl client.Client, leaseName, holder, namespace string) error {
	lease, err := GetLease(ctx, cl, leaseName, namespace)
	if k8serrors.IsNotFound(err) {
		return nil
	} else if err != nil {
		return err
	}

	if holderID := lease.Spec.HolderIdentity; holderID != nil && *holderID != holder {
		return ErrLeaseAlreadyHeld
	}

	if err := cl.Delete(ctx, lease, &client.DeleteOptions{
		Preconditions: &metav1.Preconditions{
			UID:             &lease.UID,
			ResourceVersion: &lease.ResourceVersion,
		},
	}); err != nil {
		if k8serrors.IsNotFound(err) || k8serrors.IsConflict(err) {
			return nil
		}
		return errors.Wrap(err, "delete lease")
	}
	return nil
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
