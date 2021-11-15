package reconcile

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	orclient "github.com/percona/percona-server-mysql-operator/pkg/orchestrator/client"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

type clientReadWriter interface {
	client.Reader
	client.Writer
}

func reconcileReplicationPrimaryPod(ctx context.Context, rw clientReadWriter, cr *v2.PerconaServerForMySQL) error {
	orc := orclient.New(orchestrator.APIHost(cr))
	clusterHint := cr.ClusterHint()

	podList, err := podListByLabel(ctx, rw, mysql.MatchLabels(cr))
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

		if err := rw.Update(ctx, &pod); err != nil {
			return errors.Wrap(err, "update primary pod")
		}
	}

	return nil
}

func reconcileReplicationSemiSync(ctx context.Context, rdr client.Reader, cr *v2.PerconaServerForMySQL) error {
	orc := orclient.New(orchestrator.APIHost(cr))
	clusterHint := cr.ClusterHint()

	primary, err := orc.ClusterPrimary(clusterHint)
	if err != nil {
		return errors.Wrap(err, "get primary from orchestrator")
	}

	operatorPass, err := k8s.UserPassword(ctx, rdr, cr, v2.USERS_SECRET_KEY_OPERATOR)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	db, err := replicator.NewReplicator(v2.USERS_SECRET_KEY_OPERATOR,
		operatorPass,
		primary.Key.Hostname,
		int32(33062))
	if err != nil {
		return errors.Wrapf(err, "connect to %s", primary.Key.Hostname)
	}
	defer db.Close()

	if err := db.SetSemiSyncSource(cr.Spec.MySQL.SizeSemiSync > 0); err != nil {
		return errors.Wrapf(err, "set semi-sync on %s", primary.Key.Hostname)
	}

	if cr.Spec.MySQL.SizeSemiSync < 1 {
		return nil
	}

	if err := db.SetSemiSyncSize(cr.Spec.MySQL.SizeSemiSync); err != nil {
		return errors.Wrapf(err, "set semi-sync size on %s", primary.Key.Hostname)
	}

	return nil
}

func podListByLabel(ctx context.Context, rdr client.Reader, l map[string]string) (*corev1.PodList, error) {
	podList := &corev1.PodList{}
	err := rdr.List(ctx, podList, &client.ListOptions{LabelSelector: labels.SelectorFromSet(l)})
	return podList, err
}
