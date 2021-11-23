package k8s

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type APIGetter interface {
	Get(context.Context, client.ObjectKey, client.Object) error
}

type APICreater interface {
	Create(context.Context, client.Object, ...client.CreateOption) error
}

type APIPatcher interface {
	Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error
}

type APIList interface {
	List(context.Context, client.ObjectList, ...client.ListOption) error
}

type APIGetCreatePatcher interface {
	APIGetter
	APICreater
	APIPatcher
}

type APIListPatcher interface {
	APIPatcher
	APIList
}
