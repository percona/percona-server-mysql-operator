package orchestrator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func hasEnv(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
}

func TestOrchestratorAPIAuthGate(t *testing.T) {
	t.Run("enabled from 1.2.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"

		c := container(cr)
		assert.True(t, hasEnv(c.Env, "ORC_API_AUTH"), "ORC_API_AUTH env must be set for crVersion >= 1.2.0")

		require.NotNil(t, c.ReadinessProbe.Exec, "readiness probe must be exec-based when API auth is enabled")
		cmd := strings.Join(c.ReadinessProbe.Exec.Command, " ")
		assert.Contains(t, cmd, "-u", "probe must authenticate: %s", cmd)
		assert.Contains(t, cmd, "readonly", "probe must authenticate as readonly: %s", cmd)

		assert.NotNil(t, c.LivenessProbe.Exec, "liveness probe must be exec-based when API auth is enabled")
	})

	t.Run("disabled before 1.2.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.1.0"

		c := container(cr)
		assert.False(t, hasEnv(c.Env, "ORC_API_AUTH"), "ORC_API_AUTH env must not be set for crVersion < 1.2.0")
		assert.NotNil(t, c.ReadinessProbe.HTTPGet, "readiness probe must stay httpGet before API auth")
		assert.NotNil(t, c.LivenessProbe.HTTPGet, "liveness probe must stay httpGet before API auth")
	})
}
