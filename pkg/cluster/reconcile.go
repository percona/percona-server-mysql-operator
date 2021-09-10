package cluster

import (
	"context"

	"github.com/pkg/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v2 "github.com/percona/percona-mysql/api/v2"
	"github.com/percona/percona-mysql/pkg/database"
)

type MySQLReconciler struct {
	Client client.Client
}

func (r *MySQLReconciler) Reconcile(ctx context.Context, t types.NamespacedName) error {
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

	if err := cr.CheckNSetDefaults(log); err != nil {
		return errors.Wrap(err, "wrong PS options")
	}

        if err := r.reconcileUsersSecret(cr); err != nil {
		return errors.Wrap(err, "reconcile users secret")
        }

	mysql := database.New(cr)
	sfs := mysql.StatefulSet()

	if err := r.Client.Create(context.TODO(), sfs); err != nil {
		return errors.Wrap(err, "create mysql statefulset")
	}

	return nil
}
