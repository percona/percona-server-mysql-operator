package topology

import (
	"context"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"

	"github.com/pkg/errors"
)

type Manager interface {
	Replicator(host string) (replicator.Replicator, error)
	ClusterType() apiv1alpha1.ClusterType
	Get(ctx context.Context) (Topology, error)
}

type topologyManager struct {
	operatorPass string
	clusterType  apiv1alpha1.ClusterType
}

func NewTopologyManager(clusterType apiv1alpha1.ClusterType, operatorPass string) Manager {
	return &topologyManager{
		operatorPass: operatorPass,
		clusterType:  clusterType,
	}
}

func (m *topologyManager) Replicator(hostname string) (replicator.Replicator, error) {
	return replicator.NewReplicator(apiv1alpha1.UserOperator, m.operatorPass, hostname, mysql.DefaultAdminPort)
}

func (m *topologyManager) ClusterType() apiv1alpha1.ClusterType {
	return m.clusterType
}

func (s *topologyManager) Get(ctx context.Context) (Topology, error) {
	// TODO: implement
	return Topology{}, errors.New("not implemented")
}
