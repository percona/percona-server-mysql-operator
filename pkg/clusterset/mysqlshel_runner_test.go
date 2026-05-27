package clusterset

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestMySQLShellRunner(t *testing.T) {
	pcs := &apiv1.PerconaServerMySQLClusterSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "clusterset",
			Namespace: "clusterset-ns",
		},
		Spec: apiv1.PerconaServerMySQLClusterSetSpec{
			CredentialsSecret: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "clusterset-creds"},
				Key:                  "password",
			},
			MysqlShellRunner: apiv1.MysqlShellRunner{
				Image: "percona/percona-server-mysql-operator:mysqlshell",
			},
		},
	}

	deployment := MySQLShellRunner(pcs)

	labels := map[string]string{
		"app.kubernetes.io/name":       "mysqlshell-runner",
		"app.kubernetes.io/instance":   "clusterset",
		"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
		"app.kubernetes.io/part-of":    "percona-server",
		"app.kubernetes.io/component":  "mysqlshell-runner",
	}
	assert.Equal(t, "apps/v1", deployment.APIVersion)
	assert.Equal(t, "Deployment", deployment.Kind)
	assert.Equal(t, "clusterset-runner", deployment.Name)
	assert.Equal(t, "clusterset-ns", deployment.Namespace)
	assert.Equal(t, labels, deployment.Labels)
	assert.Equal(t, int32(1), *deployment.Spec.Replicas)
	assert.Equal(t, labels, deployment.Spec.Selector.MatchLabels)
	assert.Equal(t, labels, deployment.Spec.Template.Labels)

	containers := deployment.Spec.Template.Spec.Containers
	assert.Len(t, containers, 1)
	assert.Equal(t, MySQLShellRunnerAppName, containers[0].Name)
	assert.Equal(t, "percona/percona-server-mysql-operator:mysqlshell", containers[0].Image)
	assert.Equal(t, []string{"sleep", "infinity"}, containers[0].Command)
	assert.Equal(t, corev1.RestartPolicyAlways, deployment.Spec.Template.Spec.RestartPolicy)
	assert.Equal(t, corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: "clusterset-creds"},
		Key:                  "password",
	}, *containers[0].Env[0].ValueFrom.SecretKeyRef)
}
