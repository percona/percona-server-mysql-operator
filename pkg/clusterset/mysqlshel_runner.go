package clusterset

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

const (
	MySQLShellRunnerAppName   = "mysqlshell-runner"
	MySQLShellRunnerComponent = "mysqlshell-runner"
	MySQLShellRunnerPassword  = "MYSQL_PASSWORD"
)

func MySQLShellRunner(pcs *apiv1.PerconaServerMySQLClusterSet) *appsv1.Deployment {
	matchLabels := naming.Labels(MySQLShellRunnerAppName, pcs.Name, "percona-server", MySQLShellRunnerComponent)
	depl := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "apps/v1",
			Kind:       "Deployment",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      pcs.Name + "-runner",
			Namespace: pcs.Namespace,
			Labels:    naming.Labels(MySQLShellRunnerAppName, pcs.Name, "percona-server", MySQLShellRunnerComponent),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: matchLabels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: naming.Labels(MySQLShellRunnerAppName, pcs.Name, "percona-server", MySQLShellRunnerComponent),
				},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyAlways,
					Containers: []corev1.Container{
						{
							Name:    MySQLShellRunnerAppName,
							Image:   pcs.Spec.MySQLShellRunner.Image,
							Command: []string{"sleep", "infinity"},
							Env: []corev1.EnvVar{
								{
									Name:  "HOME",
									Value: "/tmp",
								},
								{
									Name: MySQLShellRunnerPassword,
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: pcs.Spec.CredentialsSecret.DeepCopy(),
									},
								},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("100m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
							},
						},
					},
					TerminationGracePeriodSeconds: new(int64(5)),
				},
			},
		},
	}

	return depl
}
