package clusterset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestClusterSetManagerJob(t *testing.T) {
	pcs := &apiv1.PerconaServerMySQLClusterSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-set",
			Namespace: "test-ns",
		},
	}
	cluster := &apiv1.ClusterSetCluster{
		InnoDBClusterName: "replica",
	}
	args := []string{"--cluster", "replica"}

	job := ClusterSetManagerJob(pcs, cluster, "mysqlshell:latest", CmdAddReplica, args, "clusterset-manager:latest", "clusterset-manager-sa")

	expectedLabels := map[string]string{
		"app.kubernetes.io/name":       ClusterSetManagerAppName,
		"app.kubernetes.io/instance":   "cluster-set",
		"app.kubernetes.io/part-of":    "percona-server",
		"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
		"app.kubernetes.io/component":  ClusterSetManagerComponent,
		"cluster-name":                 "replica",
		"command":                      CmdAddReplica,
	}

	assert.Equal(t, "cluster-set-replica-add-replica", job.Name)
	assert.Equal(t, "test-ns", job.Namespace)
	assert.Equal(t, expectedLabels, job.Labels)
	assert.Equal(t, &batchv1.JobSpec{
		Parallelism:             ptr.To(int32(1)),
		Completions:             ptr.To(int32(1)),
		TTLSecondsAfterFinished: ptr.To(int32(90)),
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels: expectedLabels,
			},
			Spec: corev1.PodSpec{
				RestartPolicy:      corev1.RestartPolicyNever,
				ServiceAccountName: "replica-manager-sa",
				Containers: []corev1.Container{
					{
						Name:            ClusterSetManagerAppName,
						Image:           "clusterset-manager:latest",
						ImagePullPolicy: corev1.PullAlways,
						Command:         []string{clusterSetManagerBinaryPath},
						Args:            []string{CmdAddReplica, "--cluster", "replica"},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					},
				},
			},
		},
	}, &job.Spec)

	require.Len(t, job.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, []string{"--cluster", "replica"}, args)
}
