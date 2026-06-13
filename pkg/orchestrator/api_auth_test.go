package orchestrator

import (
	"strings"
	"testing"

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
		if !hasEnv(c.Env, "ORC_API_AUTH") {
			t.Fatal("ORC_API_AUTH env must be set for crVersion >= 1.2.0")
		}
		if c.ReadinessProbe.Exec == nil {
			t.Fatal("readiness probe must be exec-based when API auth is enabled")
		}
		cmd := strings.Join(c.ReadinessProbe.Exec.Command, " ")
		if !strings.Contains(cmd, "-u") || !strings.Contains(cmd, "readonly") {
			t.Fatalf("probe must authenticate as readonly: %s", cmd)
		}
		if c.LivenessProbe.Exec == nil {
			t.Fatal("liveness probe must be exec-based when API auth is enabled")
		}
	})

	t.Run("disabled before 1.2.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.1.0"

		c := container(cr)
		if hasEnv(c.Env, "ORC_API_AUTH") {
			t.Fatal("ORC_API_AUTH env must not be set for crVersion < 1.2.0")
		}
		if c.ReadinessProbe.HTTPGet == nil {
			t.Fatal("readiness probe must stay httpGet before API auth")
		}
		if c.LivenessProbe.HTTPGet == nil {
			t.Fatal("liveness probe must stay httpGet before API auth")
		}
	})
}
