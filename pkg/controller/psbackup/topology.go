package psbackup

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
	"github.com/percona/percona-server-mysql-operator/pkg/topology"
)

// getDBTopology returns the topology of the database cluster.
func getDBTopology(ctx context.Context, cli client.Client, cliCmd clientcmd.Client, cluster *apiv1.PerconaServerMySQL, operatorPass string) (topology.Topology, error) {
	switch cluster.Spec.MySQL.ClusterType {
	case apiv1.ClusterTypeGR:
		top, err := topology.GroupReplication(ctx, cli, cliCmd, cluster, operatorPass)
		if err != nil {
			return topology.Topology{}, errors.Wrapf(err, "failed to get group replication")
		}
		return top, nil
	case apiv1.ClusterTypeAsync:
		pod := &corev1.Pod{}
		nn := types.NamespacedName{Namespace: cluster.Namespace, Name: orchestrator.PodName(cluster, 0)}
		if err := cli.Get(ctx, nn, pod); err != nil {
			return topology.Topology{}, err
		}

		primary, err := orchestrator.ClusterPrimary(ctx, cliCmd, pod, cluster.ClusterHint())

		if err != nil {
			return topology.Topology{}, errors.Wrap(err, "get primary")
		}

		replicas := make([]string, 0, len(primary.Replicas))
		for _, r := range primary.Replicas {
			replicas = append(replicas, r.Hostname)
		}
		return topology.Topology{
			Primary:  primary.Key.Hostname,
			Replicas: replicas,
		}, nil
	default:
		return topology.Topology{}, errors.New("unknown cluster type")
	}
}
