package topology

import (
	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
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
