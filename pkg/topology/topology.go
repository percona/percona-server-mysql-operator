package topology

import (
	"context"
	"database/sql"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

var ErrNoPrimary = errors.New("no primary")

// Topology represents the topology of the database cluster.
type Topology struct {
	Primary  string
	Replicas []string
}

// GroupReplication returns the topology of the mysql cluster in terms of primary and replicas when the cluster type is GR.
func GroupReplication(ctx context.Context, cli client.Client, cliCmd clientcmd.Client, cluster *apiv1.PerconaServerMySQL, operatorPass string) (Topology, error) {
	logger := logf.FromContext(ctx)

	if !cluster.Spec.MySQL.IsGR() {
		return Topology{}, errors.New("cluster type is not group replication")
	}

	pod, err := getReadyPod(ctx, cli, cluster)
	if err != nil {
		logger.Info(errors.Wrap(err, "error getting ready pod").Error())
		return Topology{}, nil
	}

	fqdn := mysql.PodFQDN(cluster, &pod)

	rm := db.NewReplicationManager(&pod, cliCmd, apiv1.UserOperator, operatorPass, fqdn)

	replicas, err := rm.GetGroupReplicationReplicas(ctx)
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group replication replicas")
	}

	primary, err := rm.GetGroupReplicationPrimary(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Topology{}, ErrNoPrimary
		}
		return Topology{}, errors.Wrap(err, "get group replication primary")
	}

	return Topology{
		Primary:  primary,
		Replicas: replicas,
	}, nil
}

func getReadyPod(ctx context.Context, cli client.Client, cluster *apiv1.PerconaServerMySQL) (corev1.Pod, error) {
	pods, err := k8s.PodsByLabels(ctx, cli, mysql.MatchLabels(cluster), cluster.Namespace)
	if err != nil {
		return corev1.Pod{}, errors.Wrap(err, "get pods")
	}
	for _, pod := range pods {
		if k8s.IsPodReady(pod) {
			return pod, nil
		}
	}
	return corev1.Pod{}, errors.Errorf("no pod is ready")
}
