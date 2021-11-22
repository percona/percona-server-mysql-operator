package k8s

import (
	"context"
	"fmt"
	"os"

	"github.com/percona/percona-server-mysql-operator/pkg/util"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

const WatchNamespaceEnvVar = "WATCH_NAMESPACE"

// GetWatchNamespace returns the namespace the operator should be watching for changes
func GetWatchNamespace() (string, error) {
	ns, found := os.LookupEnv(WatchNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", WatchNamespaceEnvVar)
	}
	return ns, nil
}

func LabelsEqual(old, new metav1.Object) bool {
	return util.SSMapEqual(old.GetLabels(), new.GetLabels())
}

func RemoveLabel(obj client.Object, key string) {
	labels := obj.GetLabels()
	delete(obj.GetLabels(), key)
	obj.SetLabels(labels)
}

func AddLabel(obj client.Object, key, value string) {
	labels := obj.GetLabels()
	labels[key] = value
	obj.SetLabels(labels)
}

type Checker interface {
	CheckNSetDefaults() error
}

func GetObjectWithDefaults(ctx context.Context, get APIGetter, nn types.NamespacedName, o client.Object) (client.Object, error) {
	if err := get.Get(ctx, nn, o); err != nil {
		return nil, err
	}

	if v, ok := o.(Checker); ok {
		if err := v.CheckNSetDefaults(); err != nil {
			return o, errors.Wrap(err, "object defaults")
		}
	}

	return o, nil
}

func ObjectExists(ctx context.Context, get APIGetter, nn types.NamespacedName, o client.Object) (bool, error) {
	if err := get.Get(ctx, nn, o); err != nil {
		if k8serrors.IsNotFound(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}
