package ps

import (
	"context"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func (r *PerconaServerMySQLReconciler) reconcileMySQLConfig(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
) error {
	if cr.CompareVersion("1.2.0") < 0 {
		return nil
	}

	currentHash, err := k8s.CustomConfigHash(ctx, r.Client, cr, new(mysql.Configurable(*cr)), naming.ComponentDatabase)
	if err != nil {
		return errors.Wrap(err, "get current config hash")
	}

	persistHashToStatus := func() error {
		nn := types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}
		if err := writeStatus(ctx, r.Client, nn, func(s *apiv1.PerconaServerMySQLStatus) error {
			s.MySQL.LastAppliedConfigHash = currentHash
			return nil
		}); err != nil {
			return errors.Wrap(err, "write status")
		}
		return nil
	}

	// New cluster, the config from ConfigMap will be applied when the mysql pods start.
	// Just update the status and return.
	if cr.Status.MySQL.State == apiv1.StateNew {
		if err := persistHashToStatus(); err != nil {
			return errors.Wrap(err, "persist hash to status")
		}
	}

	if currentHash == cr.Status.MySQL.LastAppliedConfigHash {
		return nil
	}

	log := logf.FromContext(ctx)

	// Wait for cluster to be ready before applying configuration
	if cr.Status.State != apiv1.StateReady {
		log.Info("Waiting for cluster to be ready before applyin MySQL configuration")
		return nil
	}

	conf, err := mysql.GetConfig(ctx, r.APIReader, cr)
	if err != nil {
		return errors.Wrap(err, "get MySQL config")
	}

	log.Info("Applying MySQL configuration")
	restartNeeded, err := applyMySQLRuntimeConfig(ctx, r.Client, r.ClientCmd, cr, conf)
	if err != nil {
		return errors.Wrap(err, "apply MySQL runtime config")
	}

	if restartNeeded {
		log.Info("Read-only variables changed, restarting MySQL pods to apply configuration")
		sts := &appsv1.StatefulSet{}
		if err := r.Get(ctx, mysql.NamespacedName(cr), sts); err != nil {
			return errors.Wrap(err, "get MySQL statefulset")
		}

		if err := k8s.RolloutRestart(ctx, r.Client, sts, naming.AnnotationConfigHash, currentHash); err != nil {
			return errors.Wrap(err, "restart MySQL pods")
		}
	}

	// TODO: handle removal of config options

	if err := persistHashToStatus(); err != nil {
		return errors.Wrap(err, "persist hash to status")
	}

	log.Info("MySQL configuration applied successfully")
	return nil
}
