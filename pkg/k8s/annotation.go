package k8s

import (
	"context"

	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	k8sretry "k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// AnnotateObject adds the specified annotations to the object
func AnnotateObject(ctx context.Context, c client.Client, obj client.Object, annotations map[naming.AnnotationKey]string) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultBackoff, func() error {
		return annotateObject(ctx, c, obj, annotations)
	})
}

func annotateObject(ctx context.Context, c client.Client, obj client.Object, annotations map[naming.AnnotationKey]string) error {
	o := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), o)
	if err != nil {
		return err
	}

	orig := o.DeepCopyObject().(client.Object)

	a := o.GetAnnotations()
	if a == nil {
		a = make(map[string]string)
	}

	for k, v := range annotations {
		a[string(k)] = v
	}
	o.SetAnnotations(a)

	// Since we are working with a copy of an object,
	// we should annotate the current reference manually so that other reconcile functions can see it.
	obj.SetAnnotations(a)

	return c.Patch(ctx, o, client.MergeFrom(orig))
}

// DeannotateObject removes the specified annotation from the object
func DeannotateObject(ctx context.Context, c client.Client, obj client.Object, annotation naming.AnnotationKey) error {
	return k8sretry.RetryOnConflict(k8sretry.DefaultBackoff, func() error {
		return deannotateObject(ctx, c, obj, annotation)
	})
}

func deannotateObject(ctx context.Context, c client.Client, obj client.Object, annotation naming.AnnotationKey) error {
	o := obj.DeepCopyObject().(client.Object)
	err := c.Get(ctx, client.ObjectKeyFromObject(obj), o)
	if err != nil {
		return err
	}

	orig := o.DeepCopyObject().(client.Object)

	a := o.GetAnnotations()
	if a == nil {
		a = make(map[string]string)
	}

	delete(a, string(annotation))
	o.SetAnnotations(a)

	return c.Patch(ctx, o, client.MergeFrom(orig))
}
