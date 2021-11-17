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

type APIUpdater interface {
	Update(context.Context, client.Object, ...client.UpdateOption) error
}

type APIList interface {
	List(context.Context, client.ObjectList, ...client.ListOption) error
}

type APIGetCreateUpdater interface {
	APIGetter
	APICreater
	APIUpdater
}

type APIListUpdater interface {
	APIUpdater
	APIList
}
