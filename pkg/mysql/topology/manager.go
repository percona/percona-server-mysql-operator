package topology

import (
	"context"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/pkg/errors"
)

type Manager interface {
	Replicator(ctx context.Context, host string) (replicator.Replicator, error)
	ClusterType() apiv1alpha1.ClusterType
	Get(ctx context.Context) (Topology, error)
}

type topologyManager struct {
	operatorPass string
	clusterType  apiv1alpha1.ClusterType
	cluster      *apiv1alpha1.PerconaServerMySQL
	cl           client.Reader
	hosts        []string
}

func NewTopologyManager(clusterType apiv1alpha1.ClusterType, cluster *apiv1alpha1.PerconaServerMySQL, cl client.Reader, operatorPass string, hosts ...string) (Manager, error) {
	var err error
	if cl == nil {
		cl, err = k8s.NewNamespacedClient(cluster.Namespace)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create client")
		}
	}
	return &topologyManager{
		operatorPass: operatorPass,
		clusterType:  clusterType,
		cluster:      cluster,
		cl:           cl,
		hosts:        hosts,
	}, nil
}

func (m *topologyManager) Replicator(ctx context.Context, hostname string) (replicator.Replicator, error) {
	return replicator.NewReplicator(ctx, apiv1alpha1.UserOperator, m.operatorPass, hostname, mysql.DefaultAdminPort)
}

func (m *topologyManager) ClusterType() apiv1alpha1.ClusterType {
	return m.clusterType
}

func (m *topologyManager) Get(ctx context.Context) (Topology, error) {
	var err error
	switch m.ClusterType() {
	case apiv1alpha1.ClusterTypeGR:
		// TODO: Implement
		return Topology{}, errors.Wrap(err, "get group-replication topology")
	case apiv1alpha1.ClusterTypeAsync:
		if k8s.GetExperimetalTopologyOption() {
			return experimentalGetAsync(ctx, m, m.hosts...)
		}
		return getAsync(ctx, m.cluster, m.cl)
	default:
		return Topology{}, errors.New("unknown cluster type")
	}
}
