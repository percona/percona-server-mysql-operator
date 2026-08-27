package k8s

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func SetFinalizers(
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

func RemoveFinalizers(
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
