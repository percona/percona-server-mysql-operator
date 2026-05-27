package clusterset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestClusterSetReplicaManagerJob(t *testing.T) {
	pcs := &apiv1.PerconaServerMySQLClusterSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-cluster-set",
			Namespace: "cluster-ns",
		},
	}
	cluster := &apiv1.ClusterSetCluster{
		Name: "replica-cluster",
		Endpoints: []apiv1.ClusterSetClusterEndpoint{
			{
				Host: "replica.example.com",
				Port: ptr.To(int32(3307)),
			},
		},
	}

	job := ClusterSetReplicaManagerJob(pcs, cluster, CmdAddReplica, "operator-image", "operator-service-account")

	assert.Equal(t, "my-cluster-set-replica-cluster-add-replica", job.Name)
	assert.Equal(t, "cluster-ns", job.Namespace)
	assert.Equal(t, map[string]string{
		"app.kubernetes.io/name":       "clusterset-replica-manager",
		"app.kubernetes.io/instance":   "my-cluster-set",
		"app.kubernetes.io/part-of":    "percona-server",
		"app.kubernetes.io/component":  "clusterset-replica-manager",
		"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
		"cluster-name":                 "replica-cluster",
		"command":                      CmdAddReplica,
	}, job.Labels)

	require.NotNil(t, job.Spec.Parallelism)
	assert.Equal(t, int32(1), *job.Spec.Parallelism)
	require.NotNil(t, job.Spec.Completions)
	assert.Equal(t, int32(1), *job.Spec.Completions)

	podSpec := job.Spec.Template.Spec
	assert.Equal(t, corev1.RestartPolicyNever, podSpec.RestartPolicy)
	assert.Equal(t, "operator-service-account", podSpec.ServiceAccountName)
	require.Len(t, podSpec.Containers, 1)

	container := podSpec.Containers[0]
	assert.Equal(t, "clusterset-replica-manager", container.Name)
	assert.Equal(t, "operator-image", container.Image)
	assert.Equal(t, []string{"/opt/percona-server-mysql-operator/clusterset-replica-manager"}, container.Command)
	assert.Equal(t, []string{
		CmdAddReplica,
		"--replica-cluster-name=replica-cluster",
		"--replica-endpoint=replica.example.com",
		"--replica-port=3307",
		"--ps-cluster-set-name=my-cluster-set",
		"--namespace=cluster-ns",
	}, container.Args)
}
