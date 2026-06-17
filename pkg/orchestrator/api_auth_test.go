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

func onChangeArg(t *testing.T, args []string) string {
	t.Helper()
	for _, a := range args {
		if strings.HasPrefix(a, "-on-change=") {
			return strings.TrimPrefix(a, "-on-change=")
		}
	}
	require.Fail(t, "no -on-change arg found", "args: %v", args)
	return ""
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

		for _, s := range sidecarContainers(cr) {
			assert.Truef(t, hasEnv(s.Env, "ORC_API_AUTH"), "sidecar %s must set ORC_API_AUTH for crVersion >= 1.2.0", s.Name)
		}
	})

	t.Run("add_mysql_nodes script gated on 1.2.0", func(t *testing.T) {
		t.Run("staged script from 1.2.0", func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.CRVersion = "1.2.0"

			sidecars := sidecarContainers(cr)
			require.Len(t, sidecars, 1)
			assert.Equal(t, "/opt/percona/orc-add_mysql_nodes.sh", onChangeArg(t, sidecars[0].Args),
				"sidecar must use the init-container-staged auth-aware script for crVersion >= 1.2.0")
		})

		t.Run("image script before 1.2.0", func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.CRVersion = "1.1.0"

			sidecars := sidecarContainers(cr)
			require.Len(t, sidecars, 1)
			assert.Equal(t, "/usr/bin/add_mysql_nodes.sh", onChangeArg(t, sidecars[0].Args),
				"sidecar must use the orchestrator image's script for crVersion < 1.2.0")
		})
	})

	t.Run("disabled before 1.2.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.1.0"

		c := container(cr)
		assert.False(t, hasEnv(c.Env, "ORC_API_AUTH"), "ORC_API_AUTH env must not be set for crVersion < 1.2.0")
		assert.NotNil(t, c.ReadinessProbe.HTTPGet, "readiness probe must stay httpGet before API auth")
		assert.NotNil(t, c.LivenessProbe.HTTPGet, "liveness probe must stay httpGet before API auth")

		for _, s := range sidecarContainers(cr) {
			assert.Falsef(t, hasEnv(s.Env, "ORC_API_AUTH"), "sidecar %s must not set ORC_API_AUTH for crVersion < 1.2.0", s.Name)
		}
	})
}
