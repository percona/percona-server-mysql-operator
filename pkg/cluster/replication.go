package cluster

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator"
	orclient "github.com/percona/percona-server-mysql-operator/pkg/database/orchestrator/client"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
)

func (r *MySQLReconciler) reconcileReplication(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	if err := r.reconcilePrimaryPod(log, cr); err != nil {
		return errors.Wrap(err, "reconcile primary pod")
	}

	if err := r.reconcileSemiSync(log, cr); err != nil {
		return errors.Wrap(err, "reconcile semi-sync")
	}

	return nil
}

func (r *MySQLReconciler) reconcilePrimaryPod(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)
	orc := orclient.New(o.APIHost())
	clusterHint := cr.ClusterHint()

	m := mysql.New(cr)
	podList, err := r.podListByLabel(m.MatchLabels())
	if err != nil {
		return errors.Wrap(err, "get MySQL pod list")
	}

	primary, err := orc.ClusterPrimary(clusterHint)
	if err != nil {
		return errors.Wrap(err, "get cluster from orchestrator")
	}

	for _, pod := range podList.Items {
		oldLabels := k8s.CloneLabels(pod.GetLabels())

		if pod.Name == primary.InstanceAlias {
			k8s.AddLabel(&pod, v2.MySQLPrimaryLabel, "true")
		} else {
			k8s.RemoveLabel(&pod, v2.MySQLPrimaryLabel)
		}

		if k8s.IsLabelsEqual(oldLabels, pod.GetLabels()) {
			continue
		}

		if err := r.Client.Update(context.TODO(), &pod); err != nil {
			return errors.Wrap(err, "update primary pod")
		}
	}

	return nil
}

func (r *MySQLReconciler) reconcileSemiSync(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	o := orchestrator.New(cr)
	orc := orclient.New(o.APIHost())
	clusterHint := cr.ClusterHint()

	primary, err := orc.ClusterPrimary(clusterHint)
	if err != nil {
		return errors.Wrap(err, "get primary from orchestrator")
	}

	operatorPass, err := k8s.UserPassword(r.Client, cr, v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	m, err := mysql.NewReplicator(v2.USERS_SECRET_KEY_OPERATOR, operatorPass, primary.Key.Hostname, int32(33062))
	if err != nil {
		return errors.Wrapf(err, "connect to %s", primary.Key.Hostname)
	}

	if err := m.SetSemiSyncSource(cr.Spec.MySQL.SizeSemiSync > 0); err != nil {
		return errors.Wrapf(err, "set semi-sync on %s", primary.Key.Hostname)
	}

	if cr.Spec.MySQL.SizeSemiSync < 1 {
		return nil
	}

	if err := m.SetSemiSyncSize(cr.Spec.MySQL.SizeSemiSync); err != nil {
		return errors.Wrapf(err, "set semi-sync size on %s", primary.Key.Hostname)
	}

	return nil
}

func (r *MySQLReconciler) podListByLabel(l map[string]string) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	err := r.Client.List(context.TODO(), podList, &client.ListOptions{LabelSelector: labels.SelectorFromSet(l)})
	return podList, err
}
