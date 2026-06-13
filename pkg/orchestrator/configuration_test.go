package orchestrator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestConfigMapDataUserConfiguration(t *testing.T) {
	parse := func(t *testing.T, cr *apiv1.PerconaServerMySQL) map[string]interface{} {
		data, err := ConfigMapData(cr)
		require.NoError(t, err)
		out := map[string]interface{}{}
		require.NoError(t, json.Unmarshal([]byte(data), &out))
		return out
	}

	t.Run("user keys are merged", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Configuration = `{"FailMasterPromotionOnLagMinutes": 30, "RecoveryPeriodBlockSeconds": 300}`

		cfg := parse(t, cr)
		assert.EqualValues(t, 30, cfg["FailMasterPromotionOnLagMinutes"])
		assert.EqualValues(t, 300, cfg["RecoveryPeriodBlockSeconds"])
	})

	t.Run("reserved keys cannot be overridden", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.SSLSecretName = "ssl"
		cr.Spec.Orchestrator.Size = 3
		// every reserved key a user might try to set
		cr.Spec.Orchestrator.Configuration = `{
			"RaftNodes": ["evil"],
			"RaftEnabledSingleNode": true,
			"HTTPAdvertise": "http://evil:3000",
			"RaftAdvertise": "evil",
			"RaftBind": "evil",
			"RaftEnabled": false,
			"MySQLTopologyUseMutualTLS": false,
			"MySQLTopologySSLSkipVerify": false,
			"MySQLTopologySSLPrivateKeyFile": "/evil",
			"MySQLTopologySSLCertFile": "/evil",
			"MySQLTopologySSLCAFile": "/evil",
			"AuthenticationMethod": "none",
			"HTTPAuthUser": "evil",
			"HTTPAuthPassword": "evil",
			"InstancePollSeconds": 7
		}`

		cfg := parse(t, cr)

		// operator-managed values win
		assert.NotEqual(t, []interface{}{"evil"}, cfg["RaftNodes"])
		assert.EqualValues(t, false, cfg["RaftEnabledSingleNode"])
		assert.EqualValues(t, true, cfg["MySQLTopologyUseMutualTLS"])
		assert.EqualValues(t, "/etc/orchestrator/ssl/tls.key", cfg["MySQLTopologySSLPrivateKeyFile"])

		// entrypoint-injected keys are not written to the ConfigMap at all
		for _, k := range []string{"HTTPAdvertise", "RaftAdvertise", "RaftBind", "RaftEnabled", "AuthenticationMethod", "HTTPAuthUser", "HTTPAuthPassword"} {
			_, ok := cfg[k]
			assert.Falsef(t, ok, "reserved key %s must not be in the ConfigMap", k)
		}

		// a non-reserved key still gets through
		assert.EqualValues(t, 7, cfg["InstancePollSeconds"])
	})
}
