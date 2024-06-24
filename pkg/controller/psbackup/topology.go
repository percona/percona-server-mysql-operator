package psbackup

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

// topology represents the topology of the database cluster.
type topology struct {
	primary  string
	replicas []string
}

// getDBTopology returns the topology of the database cluster.
func getDBTopology(ctx context.Context, cli client.Client, cliCmd clientcmd.Client, cluster *apiv1alpha1.PerconaServerMySQL, operatorPass string) (topology, error) {
	switch cluster.Spec.MySQL.ClusterType {
	case apiv1alpha1.ClusterTypeGR:
		firstPod := &corev1.Pod{}
		nn := types.NamespacedName{Namespace: cluster.Namespace, Name: mysql.PodName(cluster, 0)}
		if err := cli.Get(ctx, nn, firstPod); err != nil {
			return topology{}, err
		}

		fqdn := mysql.FQDN(cluster, 0)

		rm := db.NewReplicationManager(firstPod, cliCmd, apiv1alpha1.UserOperator, operatorPass, fqdn)

		replicas, err := rm.GetGroupReplicationReplicas(ctx)
		if err != nil {
			return topology{}, errors.Wrap(err, "get group-replication replicas")
		}

		primary, err := rm.GetGroupReplicationPrimary(ctx)
		if err != nil {
			return topology{}, errors.Wrap(err, "get group-replication primary")
		}
		return topology{
			primary:  primary,
			replicas: replicas,
		}, nil
	case apiv1alpha1.ClusterTypeAsync:
		pod := &corev1.Pod{}
		nn := types.NamespacedName{Namespace: cluster.Namespace, Name: orchestrator.PodName(cluster, 0)}
		if err := cli.Get(ctx, nn, pod); err != nil {
			return topology{}, err
		}

		primary, err := orchestrator.ClusterPrimary(ctx, cliCmd, pod, cluster.ClusterHint())

		if err != nil {
			return topology{}, errors.Wrap(err, "get primary")
		}

		replicas := make([]string, 0, len(primary.Replicas))
		for _, r := range primary.Replicas {
			replicas = append(replicas, r.Hostname)
		}
		return topology{
			primary:  primary.Key.Hostname,
			replicas: replicas,
		}, nil
	default:
		return topology{}, errors.New("unknown cluster type")
	}
}
