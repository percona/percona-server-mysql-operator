package clusterset

import (
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const (
	ClusterSetReplicaInitAppName    = "clusterset-replica-init"
	ClusterSetReplicaInitComponent  = "clusterset-replica-init"
	clusterSetReplicaInitBinaryPath = "/opt/percona/clusterset-replica-init"
)

func ClusterSetReplicaInitJob(
	pcs *apiv1.PerconaServerMySQLClusterSet,
	cluster *apiv1.ClusterSetCluster,
	image, serviceAccount string) *batchv1.Job {
	labels := naming.Labels(ClusterSetReplicaInitAppName, pcs.Name, "percona-server", ClusterSetReplicaInitComponent)
	endpoint := cluster.Endpoints[0]
	port := int32(3306)
	if endpoint.Port != nil {
		port = *endpoint.Port
	}

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%s-replica-init", pcs.Name, cluster.Name),
			Namespace: pcs.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Parallelism: ptr.To(int32(1)),
			Completions: ptr.To(int32(1)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: serviceAccount,
					Containers: []corev1.Container{
						{
							Name:    ClusterSetReplicaInitAppName,
							Image:   image,
							Command: []string{clusterSetReplicaInitBinaryPath},
							Args: []string{
								fmt.Sprintf("--replica-cluster-name=%s", cluster.Name),
								fmt.Sprintf("--replica-endpoint=%s", endpoint.Host),
								fmt.Sprintf("--replica-port=%d", port),
								fmt.Sprintf("--ps-cluster-set-name=%s", pcs.Name),
								fmt.Sprintf("--namespace=%s", pcs.Namespace),
							},
						},
					},
				},
			},
		},
	}
}
