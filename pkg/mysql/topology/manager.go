package topology

import (
	"context"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
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
	operatorPass    string
	clusterType     apiv1alpha1.ClusterType
	cluster         *apiv1alpha1.PerconaServerMySQL
	cl              client.Reader
	cliCmd          clientcmd.Client
	hosts           []string
	useOrchestrator bool
}

func NewTopologyManager(clusterType apiv1alpha1.ClusterType, cluster *apiv1alpha1.PerconaServerMySQL, operatorPass string, hosts ...string) *topologyManager {
	return &topologyManager{
		operatorPass:    operatorPass,
		clusterType:     clusterType,
		cluster:         cluster,
		hosts:           hosts,
		useOrchestrator: true,
	}
}

func (m *topologyManager) DisableOrchestrator(disable bool) *topologyManager {
	m.useOrchestrator = !disable
	return m
}

func (m *topologyManager) WithClientCmd(cliCmd clientcmd.Client) *topologyManager {
	m.cliCmd = cliCmd
	return m
}

func (m *topologyManager) WithClient(cl client.Reader) *topologyManager {
	m.cl = cl
	return m
}

func (m *topologyManager) Manager() (Manager, error) {
	var err error
	if m.cliCmd == nil {
		m.cliCmd, err = clientcmd.NewClient()
		if err != nil {
			return nil, errors.Wrap(err, "failed to create clientcmd")
		}
	}
	if m.cl == nil {
		m.cl, err = k8s.NewNamespacedClient(m.cluster.Namespace)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create client")
		}
	}
	return m, nil
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
		if !m.useOrchestrator {
			return getAsyncWithoutOrchestrator(ctx, m, m.hosts...)
		}
		return getAsync(ctx, m.cluster, m.cliCmd, m.cl)
	default:
		return Topology{}, errors.New("unknown cluster type")
	}
}
