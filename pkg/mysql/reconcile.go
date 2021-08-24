package mysql

import (
	"context"

	v2 "github.com/percona/percona-xtradb-cluster-operator/api/v2"
	"github.com/pkg/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type MysqlReconciler struct {
	Client client.Client
}

func (r *MysqlReconciler) Reconcile(ctx context.Context, t types.NamespacedName) error {
	log := log.FromContext(ctx).WithName("PerconaServerForMySQL").WithValues("name", t.Name, "namespace", t.Namespace)

	cr := &v2.PerconaServerForMySQL{}
	err := r.Client.Get(ctx, t, cr)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			return nil
		}
		return errors.Wrapf(err, "get cluster with name %s in namespace %s", t.Name, t.Namespace)
	}

	cr.CheckNSetDefaults(log)

	return nil
}
