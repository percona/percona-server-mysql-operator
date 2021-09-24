package cluster

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v2 "github.com/percona/percona-server-mysql-operator/pkg/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/database/mysql"
)

func (r *MySQLReconciler) reconcileReplication(log logr.Logger, cr *v2.PerconaServerForMySQL) error {
	m := mysql.New(cr)

	pods := corev1.PodList{}
	if err := r.Client.List(context.TODO(), &pods, &client.ListOptions{
		Namespace:     m.Namespace,
		LabelSelector: labels.SelectorFromSet(m.MatchLabels()),
	}); err != nil {
		return errors.Wrap(err, "list MySQL pods")
	}

	if len(pods.Items) < int(m.Size) {
		return nil
	}

	usersSecret := &corev1.Secret{}
	if err := r.Client.Get(context.TODO(),
		types.NamespacedName{Name: cr.Spec.SecretsName, Namespace: cr.Namespace},
		usersSecret,
	); err != nil {
		return errors.Wrapf(err, "get %s/%s", usersSecret.Kind, cr.Spec.SecretsName)
	}

	pass := string(usersSecret.Data[v2.USERS_SECRET_KEY_OPERATOR])
	replicaPass := string(usersSecret.Data[v2.USERS_SECRET_KEY_REPLICATION])
	host := m.Name + "-0." + m.Name + "." + m.Namespace

	db, err := mysql.NewConnection(v2.USERS_SECRET_KEY_OPERATOR, pass, host, int32(3306))
	if err != nil {
		return errors.Wrapf(err, "new connection to %s", host)
	}

	ch := mysql.ReplicationChannel{
		Name:   mysql.DefaultChannelName,
		Host:   host,
		Port:   3306,
		Weight: 1,
	}

	if err := db.AddReplicationSource(ch); err != nil {
		return errors.Wrapf(err, "add replication source on %s", host)
	}

	if err := db.Close(); err != nil {
		return errors.Wrapf(err, "close connection to %s", host)
	}

	for i := int32(1); i < m.Size; i++ {
		host := fmt.Sprintf("%s-%d.%s.%s", m.Name, i, m.Name, m.Namespace)
		db, err := mysql.NewConnection(v2.USERS_SECRET_KEY_OPERATOR, pass, host, int32(3306))
		if err != nil {
			return errors.Wrapf(err, "new connection to %s", host)
		}

		replicationStatus, err := db.ReplicationStatus(ch.Name)
		if err != nil {
			return errors.Wrapf(err, "check replication status on %s", host)
		}

		if replicationStatus != mysql.ReplicationStatusNotInitiated {
			continue
		}

		if err := db.StartReplication(ch, replicaPass); err != nil {
			return errors.Wrapf(err, "start replica on %s", host)
		}
		if err := db.Close(); err != nil {
			return errors.Wrapf(err, "close connection to %s", host)
		}
	}

	return nil
}
