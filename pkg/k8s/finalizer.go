package k8s

import (
	"context"

	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func RemoveFinalizers(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	finalizers ...string,
) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultBackoff, func() error {
		return removeFinalizers(ctx, c, obj, finalizers...)
	})
}

func SetFinalizers(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	finalizers ...string,
) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultBackoff, func() error {
		return setFinalizers(ctx, c, obj, finalizers...)
	})
}

func setFinalizers(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	finalizers ...string,
) error {
	o := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), o)
	if err != nil {
		return err
	}

	orig := o.DeepCopyObject().(client.Object)
	updateNeeded := false
	for _, f := range finalizers {
		if controllerutil.AddFinalizer(o, f) {
			updateNeeded = true
		}
	}

	if !updateNeeded {
		return nil
	}
	obj.SetFinalizers(o.GetFinalizers())
	return c.Patch(ctx, o, client.MergeFrom(orig))
}

func removeFinalizers(
	ctx context.Context,
	c client.Client,
	obj client.Object,
	finalizers ...string,
) error {
	o := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), o)
	if err != nil {
		return err
	}

	orig := o.DeepCopyObject().(client.Object)
	updateNeeded := false
	for _, f := range finalizers {
		if controllerutil.RemoveFinalizer(o, f) {
			updateNeeded = true
		}
	}

	if !updateNeeded {
		return nil
	}
	obj.SetFinalizers(o.GetFinalizers())
	return c.Patch(ctx, o, client.MergeFrom(orig))
}
