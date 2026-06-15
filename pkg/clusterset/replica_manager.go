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
	ClusterSetManagerAppName    = "clusterset-manager"
	ClusterSetManagerComponent  = "clusterset-manager"
	clusterSetManagerBinaryPath = "/opt/percona-server-mysql-operator/clusterset-manager"

	CmdAddReplica       = "add-replica"
	CmdRemoveReplica    = "remove-replica"
	CmdSetPrimary       = "set-primary"
	CmdForcePrimary     = "force-primary"
	CmdCreateClusterSet = "create-cluster-set"
)

func ClusterSetManagerJob(
	pcs *apiv1.PerconaServerMySQLClusterSet,
	cluster *apiv1.ClusterSetCluster,
	mysqlShellImage string,
	cmd string,
	args []string,
	image, serviceAccount string,
) *batchv1.Job {
	labels := naming.Labels(ClusterSetManagerAppName, pcs.Name, "percona-server", ClusterSetManagerComponent)
	labels["cluster-name"] = cluster.InnoDBClusterName
	labels["command"] = cmd

	args = append([]string{cmd}, args...)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-%s", pcs.Name, cluster.InnoDBClusterName, cmd),
			Namespace: pcs.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: new(int32(3)),
			Parallelism:  new(int32(1)),
			Completions:  new(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: serviceAccount,
					// Native sidecar
					InitContainers: []corev1.Container{
						{
							Name:            "mysqlshell",
							Image:           mysqlShellImage,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"sleep", "infinity"},
							RestartPolicy:   new(corev1.ContainerRestartPolicyAlways),
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
					Containers: []corev1.Container{
						{
							Name:            ClusterSetManagerAppName,
							Image:           image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{clusterSetManagerBinaryPath},
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

							Env: []corev1.EnvVar{
								{
									Name: "POD_NAME",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.name",
										},
									},
								},
								{
									Name: "POD_NAMESPACE",
									ValueFrom: &corev1.EnvVarSource{
										FieldRef: &corev1.ObjectFieldSelector{
											FieldPath: "metadata.namespace",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
