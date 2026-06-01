package clusterset

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const (
	ClusterSetReplicaManagerAppName    = "clusterset-replica-manager"
	ClusterSetReplicaManagerComponent  = "clusterset-replica-manager"
	clusterSetReplicaManagerBinaryPath = "/opt/percona-server-mysql-operator/clusterset-replica-manager"

	CmdAddReplica    = "add-replica"
	CmdRemoveReplica = "remove-replica"
	CmdSetPrimary    = "set-primary"
)

func ClusterSetReplicaManagerJob(
	pcs *apiv1.PerconaServerMySQLClusterSet,
	cluster *apiv1.ClusterSetCluster,
	cmd string,
	args []string,
	image, serviceAccount string,
) *batchv1.Job {
	labels := naming.Labels(ClusterSetReplicaManagerAppName, pcs.Name, "percona-server", ClusterSetReplicaManagerComponent)
	labels["cluster-name"] = cluster.Name
	labels["command"] = cmd

	args = append([]string{cmd}, args...)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%s", pcs.Name, cluster.Name, cmd),
			Namespace: pcs.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Parallelism:             new(int32(1)),
			Completions:             new(int32(1)),
			TTLSecondsAfterFinished: new(int32(90)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: serviceAccount,
					Containers: []corev1.Container{
						{
							Name:            ClusterSetReplicaManagerAppName,
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{clusterSetReplicaManagerBinaryPath},
							Args:            args,
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
		},
	}
}
