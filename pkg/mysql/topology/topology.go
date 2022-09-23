package topology

import (
	"context"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

type Topology struct {
	Primary  string
	Replicas []string
}

func Get(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL, operatorPass string) (Topology, error) {
	var err error
	var top Topology
	switch cluster.Spec.MySQL.ClusterType {
	case apiv1alpha1.ClusterTypeGR:
		top, err = getGRTopology(cluster, operatorPass)
		if err != nil {
			return Topology{}, errors.Wrap(err, "get group-replication topology")
		}
	case apiv1alpha1.ClusterTypeAsync:
		top, err = getAsyncTopology(ctx, cluster)
		if err != nil {
			return Topology{}, errors.Wrap(err, "get async topology")
		}
	default:
		return Topology{}, errors.New("unknown cluster type")
	}
	return top, nil
}

func getGRTopology(cluster *apiv1alpha1.PerconaServerMySQL, operatorPass string) (Topology, error) {
	fqdn := mysql.FQDN(cluster, 0)
	db, err := replicator.NewReplicator(apiv1alpha1.UserOperator, operatorPass, fqdn, mysql.DefaultAdminPort)
	if err != nil {
		return Topology{}, errors.Wrapf(err, "open connection to %s", fqdn)
	}
	defer db.Close()

	replicas, err := db.GetGroupReplicationReplicas()
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication replicas")
	}

	primary, err := db.GetGroupReplicationPrimary()
	if err != nil {
		return Topology{}, errors.Wrap(err, "get group-replication primary")
	}
	return Topology{
		Primary:  primary,
		Replicas: replicas,
	}, nil
}

func getAsyncTopology(ctx context.Context, cluster *apiv1alpha1.PerconaServerMySQL) (Topology, error) {
	orcHost := orchestrator.APIHost(cluster)
	primary, err := orchestrator.ClusterPrimary(ctx, orcHost, cluster.ClusterHint())
	if err != nil {
		return Topology{}, errors.Wrap(err, "get primary")
	}

	replicas := make([]string, 0, len(primary.Replicas))
	for _, r := range primary.Replicas {
		replicas = append(replicas, r.Hostname)
	}
	return Topology{
		Primary:  primary.Key.Hostname,
		Replicas: replicas,
	}, nil
}
