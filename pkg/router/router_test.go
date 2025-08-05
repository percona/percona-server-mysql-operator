package router

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

func TestDeployment(t *testing.T) {
	const ns = "router-ns"

	const initImage = "init-image"
	const configHash = "config-hash"
	const tlsHash = "config-hash"

	cr := readDefaultCluster(t, "cluster", ns)
	if err := cr.CheckNSetDefaults(t.Context(), &platform.ServerVersion{
		Platform: platform.PlatformKubernetes,
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("object meta", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)

		assert.NotNil(t, deployment)
		assert.Equal(t, "cluster-router", deployment.Name)
		assert.Equal(t, "router-ns", deployment.Namespace)
		labels := map[string]string{
			"app.kubernetes.io/name":       "router",
			"app.kubernetes.io/part-of":    "percona-server",
			"app.kubernetes.io/version":    "v" + version.Version(),
			"app.kubernetes.io/instance":   "cluster",
			"app.kubernetes.io/managed-by": "percona-server-mysql-operator",
			"app.kubernetes.io/component":  "proxy",
		}
		assert.Equal(t, labels, deployment.Labels)
	})

	t.Run("defaults", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)

		assert.Equal(t, int32(3), *deployment.Spec.Replicas)
		initContainers := deployment.Spec.Template.Spec.InitContainers
		assert.Len(t, initContainers, 1)
		assert.Equal(t, initImage, initContainers[0].Image)

		assert.Equal(t, map[string]string{
			"percona.com/last-applied-tls":   tlsHash,
			"percona.com/configuration-hash": configHash,
		}, deployment.Spec.Template.Annotations)
	})

	t.Run("image pull secrets", func(t *testing.T) {
		cluster := cr.DeepCopy()

		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, []corev1.LocalObjectReference(nil), deployment.Spec.Template.Spec.ImagePullSecrets)

		imagePullSecrets := []corev1.LocalObjectReference{
			{
				Name: "secret-1",
			},
			{
				Name: "secret-2",
			},
		}
		cluster.Spec.Proxy.Router.ImagePullSecrets = imagePullSecrets

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, imagePullSecrets, deployment.Spec.Template.Spec.ImagePullSecrets)
	})

	t.Run("runtime class name", func(t *testing.T) {
		cluster := cr.DeepCopy()
		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		var e *string
		assert.Equal(t, e, deployment.Spec.Template.Spec.RuntimeClassName)

		const runtimeClassName = "runtimeClassName"
		cluster.Spec.Proxy.Router.RuntimeClassName = ptr.To(runtimeClassName)

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, runtimeClassName, *deployment.Spec.Template.Spec.RuntimeClassName)
	})

	t.Run("tolerations", func(t *testing.T) {
		cluster := cr.DeepCopy()
		deployment := Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, []corev1.Toleration(nil), deployment.Spec.Template.Spec.Tolerations)

		tolerations := []corev1.Toleration{
			{
				Key:               "node.alpha.kubernetes.io/unreachable",
				Operator:          "Exideployment",
				Value:             "value",
				Effect:            "NoExecute",
				TolerationSeconds: ptr.To(int64(1001)),
			},
		}
		cluster.Spec.Proxy.Router.Tolerations = tolerations

		deployment = Deployment(cluster, initImage, configHash, tlsHash)
		assert.Equal(t, tolerations, deployment.Spec.Template.Spec.Tolerations)
	})
}
