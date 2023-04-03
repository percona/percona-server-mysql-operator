package topology

import (
	"context"

	"github.com/pkg/errors"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
)

type Topology struct {
	Primary  string
	Replicas []string
}

func (t *Topology) AddReplica(host string) {
	if host == "" || t.Primary == host {
		return
	}
	for _, replica := range t.Replicas {
		if replica == host {
			return
		}
	}
	t.Replicas = append(t.Replicas, host)
}

func (t *Topology) SetPrimary(host string) {
	if host == "" {
		return
	}
	t.RemoveReplica(host)
	t.Primary = host
}

func (t *Topology) IsPrimary(host string) bool {
	return t.Primary == host
}

func (t *Topology) RemoveReplica(host string) {
	newReplicas := make([]string, 0, len(t.Replicas))
	for _, replica := range t.Replicas {
		if replica != host {
			newReplicas = append(newReplicas, replica)
		}
	}
	t.Replicas = newReplicas
}

func (t *Topology) HasReplica(host string) bool {
	for _, replica := range t.Replicas {
		if replica == host {
			return true
		}
	}
	return false
}

func (t *Topology) AddUnreadyPods(cluster *apiv1alpha1.PerconaServerMySQL) {
	for i := 0; i < int(cluster.MySQLSpec().Size); i++ {
		fqdn := mysql.FQDN(cluster, i)
		if t.Primary == fqdn || t.HasReplica(fqdn) {
			continue
		}
		t.AddReplica(fqdn)
	}
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
		top, err = GetAsync(ctx, operatorPass, mysql.ServiceName(cluster))
		if err != nil {
			return Topology{}, err
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
