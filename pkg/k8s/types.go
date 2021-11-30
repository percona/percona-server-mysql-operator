package k8s

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type APIGetter interface {
	Get(context.Context, client.ObjectKey, client.Object) error
}

type APICreator interface {
	Create(context.Context, client.Object, ...client.CreateOption) error
}

type APIUpdater interface {
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
}

type APIPatcher interface {
	Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

type APIList interface {
	List(context.Context, client.ObjectList, ...client.ListOption) error
}

type APIGetCreatePatcher interface {
	APIGetter
	APICreator
	APIPatcher
}

type APIListUpdater interface {
	APIUpdater
	APIList
}
