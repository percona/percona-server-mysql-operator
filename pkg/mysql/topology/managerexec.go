package topology

import (
	"context"
	"strings"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/replicator"

	"github.com/pkg/errors"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type topologyManagerExec struct {
	operatorPass string
	clusterType  apiv1alpha1.ClusterType
	cluster      *apiv1alpha1.PerconaServerMySQL
	cl           client.Reader
}

func NewTopologyManagerExec(cluster *apiv1alpha1.PerconaServerMySQL, cl client.Reader, operatorPass string) Manager {
	return &topologyManagerExec{
		operatorPass: operatorPass,
		clusterType:  cluster.Spec.MySQL.ClusterType,
		cluster:      cluster,
		cl:           cl,
	}
}

func (m *topologyManagerExec) Replicator(hostname string) (replicator.Replicator, error) {
	if hostname == mysql.ServiceName(m.cluster) {
		return replicator.NewReplicatorExec(mysql.HeadlessService(m.cluster), apiv1alpha1.UserOperator, m.operatorPass, "localhost")
	}

	pod, err := getPodByFQDN(context.TODO(), m.cl, hostname)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get pod")
	}

	return replicator.NewReplicatorExec(pod, apiv1alpha1.UserOperator, m.operatorPass, "localhost")
}

func getPodByFQDN(ctx context.Context, cl client.Reader, fqdn string) (*corev1.Pod, error) {
	fqdnSplit := strings.Split(fqdn, ".")
	if len(fqdnSplit) < 3 {
		return nil, errors.Errorf("invalid FQDN: %s", fqdn)
	}
	podName := fqdnSplit[0]
	namespace := fqdnSplit[2]
	pod := new(corev1.Pod)
	err := cl.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      podName,
	}, pod)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get pod %s", fqdn)
	}
	return pod, nil
}

func (m *topologyManagerExec) ClusterType() apiv1alpha1.ClusterType {
	return m.clusterType
}

func (m *topologyManagerExec) Get(ctx context.Context) (Topology, error) {
	var err error
	var top Topology
	switch m.ClusterType() {
	case apiv1alpha1.ClusterTypeGR:
		top, err = getGRTopology(m, mysql.FQDN(m.cluster, 0))
		if err != nil {
			return Topology{}, errors.Wrap(err, "get group-replication topology")
		}
	case apiv1alpha1.ClusterTypeAsync:
		top, err = GetAsync(ctx, m, mysql.ServiceName(m.cluster))
		if err != nil {
			return Topology{}, err
		}
	default:
		return Topology{}, errors.New("unknown cluster type")
	}
	return top, nil
}
