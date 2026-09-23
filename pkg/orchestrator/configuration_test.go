package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestConfigMapDataUserConfiguration(t *testing.T) {
	parse := func(t *testing.T, cr *apiv1.PerconaServerMySQL) map[string]any {
		data, err := ConfigMapData(cr)
		require.NoError(t, err)
		out := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(data), &out))
		return out
	}

	t.Run("user keys are merged", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Configuration = `{"UnseenInstanceForgetHours": 3, "RecoveryPeriodBlockSeconds": 300}`

		cfg := parse(t, cr)
		assert.EqualValues(t, 3, cfg["UnseenInstanceForgetHours"])
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
		assert.NotEqual(t, []any{"evil"}, cfg["RaftNodes"])
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

	t.Run("operator-critical baked defaults cannot be overridden", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Configuration = `{
			"PreFailoverProcesses": ["echo pwned"],
			"PostUnsuccessfulFailoverProcesses": ["echo pwned"],
			"FailMasterPromotionOnLagMinutes": 30,
			"DelayMasterPromotionIfSQLThreadNotUpToDate": true,
			"ReasonableMaintenanceReplicationLagSeconds": 1200,
			"PostFailoverProcesses": ["echo pwned"],
			"PostMasterFailoverProcesses": ["echo pwned"],
			"PostIntermediateMasterFailoverProcesses": ["echo pwned"],
			"PostGracefulTakeoverProcesses": ["echo pwned"],
			"DetectClusterAliasQuery": "SELECT 'evil'",
			"DetectInstanceAliasQuery": "SELECT 'evil'",
			"MySQLHostnameResolveMethod": "evil",
			"HostnameResolveMethod": "evil",
			"ListenAddress": ":1234",
			"MySQLTopologyCredentialsConfigFile": "/evil",
			"RaftDataDir": "/evil",
			"SQLite3DataFile": "/evil",
			"BackendDB": "evil",
			"ApplyMySQLPromotionAfterMasterFailover": false,
			"MasterFailoverDetachReplicaMasterHost": false,
			"DetachLostReplicasAfterMasterFailover": false,
			"FailMasterPromotionIfSQLThreadNotUpToDate": false,
			"UseSuperReadOnly": false,
			"InstancePollSeconds": 9
		}`

		cfg := parse(t, cr)

		// these are baked defaults the operator depends on; the operator does not
		// write them to the ConfigMap, so a reserved user value is simply dropped
		// and the baked default keeps winning at entrypoint merge time.
		for _, k := range []string{
			"PostMasterFailoverProcesses",
			"PostIntermediateMasterFailoverProcesses", "PostGracefulTakeoverProcesses",
			"DetectClusterAliasQuery", "DetectInstanceAliasQuery",
			"ListenAddress", "MySQLTopologyCredentialsConfigFile",
			"RaftDataDir", "SQLite3DataFile", "BackendDB",
			"ApplyMySQLPromotionAfterMasterFailover", "MasterFailoverDetachReplicaMasterHost",
			"DetachLostReplicasAfterMasterFailover", "FailMasterPromotionIfSQLThreadNotUpToDate",
			// Re-arming orchestrator's own promotion gates would veto exactly the
			// failovers the hook just made safe.
			"FailMasterPromotionOnLagMinutes", "DelayMasterPromotionIfSQLThreadNotUpToDate",
		} {
			_, ok := cfg[k]
			assert.Falsef(t, ok, "reserved key %s must not be in the ConfigMap", k)
		}

		// these the operator writes itself, so the user value must be replaced
		// rather than merely dropped
		assert.NotContains(t, cfg["PreFailoverProcesses"], "echo pwned")
		assert.NotContains(t, cfg["PostFailoverProcesses"], "echo pwned")
		assert.NotContains(t, cfg["PostUnsuccessfulFailoverProcesses"], "echo pwned")
		assert.EqualValues(t, 300, cfg["ReasonableMaintenanceReplicationLagSeconds"])

		// non-reserved keys (incl. the now-overridable ones) get through
		assert.EqualValues(t, 9, cfg["InstancePollSeconds"])
		assert.EqualValues(t, "evil", cfg["MySQLHostnameResolveMethod"])
		assert.EqualValues(t, "evil", cfg["HostnameResolveMethod"])
		assert.EqualValues(t, false, cfg["UseSuperReadOnly"])
	})
}

func TestBakedConfigDisablesPromotionLagVeto(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "orchestrator.conf.json"))
	require.NoError(t, err)

	cfg := map[string]any{}
	require.NoError(t, json.Unmarshal(data, &cfg))

	assert.EqualValues(t, 0, cfg["FailMasterPromotionOnLagMinutes"])
}

func TestBakedConfigPassesCommandHintToFailoverHook(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "orchestrator.conf.json"))
	require.NoError(t, err)

	cfg := struct {
		PreFailoverProcesses []string
	}{}
	require.NoError(t, json.Unmarshal(data, &cfg))

	var hook string
	for _, p := range cfg.PreFailoverProcesses {
		if strings.Contains(p, "orc-handler failover") {
			hook = p
		}
	}

	require.NotEmpty(t, hook, "PreFailoverProcesses must invoke the failover hook")
	assert.Contains(t, hook, "-command '{command}'")
}

func TestBakedConfigGivesTheTakeoverTimeToCatchUp(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "orchestrator.conf.json"))
	require.NoError(t, err)

	cfg := map[string]any{}
	require.NoError(t, json.Unmarshal(data, &cfg))

	assert.EqualValues(t, 300, cfg["ReasonableMaintenanceReplicationLagSeconds"])
}

func TestConfigMapDataRendersFailoverHook(t *testing.T) {
	parse := func(t *testing.T, cr *apiv1.PerconaServerMySQL) map[string]any {
		data, err := ConfigMapData(cr)
		require.NoError(t, err)
		out := map[string]any{}
		require.NoError(t, json.Unmarshal([]byte(data), &out))
		return out
	}

	hookOf := func(t *testing.T, cfg map[string]any) string {
		processes, ok := cfg["PreFailoverProcesses"].([]any)
		require.True(t, ok, "PreFailoverProcesses must be a list")
		for _, p := range processes {
			if s, ok := p.(string); ok && strings.Contains(s, "orc-handler failover") {
				return s
			}
		}
		t.Fatal("PreFailoverProcesses must invoke the failover hook")
		return ""
	}

	t.Run("defaults", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"

		cfg := parse(t, cr)
		hook := hookOf(t, cfg)

		assert.Contains(t, hook, "-timeout 6h0m0s")
		assert.Contains(t, hook, "-on-timeout Abort")
		// {command} is what tells a planned takeover from a primary that is
		// really gone; the hook must keep receiving it.
		assert.Contains(t, hook, "-command '{command}'")
		// {recoveryUID} is what the hook claims the source under and what the
		// post hooks release and acknowledge.
		assert.Contains(t, hook, "-uid {recoveryUID}")
		assert.EqualValues(t, 300, cfg["ReasonableMaintenanceReplicationLagSeconds"])
	})

	t.Run("every post hook finishes the recovery", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"

		cfg := parse(t, cr)

		for _, key := range []string{"PostFailoverProcesses", "PostUnsuccessfulFailoverProcesses"} {
			processes, ok := cfg[key].([]any)
			require.True(t, ok, "%s must be a list", key)
			require.NotEmpty(t, processes)

			last, ok := processes[len(processes)-1].(string)
			require.True(t, ok)
			assert.Contains(t, last, "orc-handler finish -source {failedHost} -uid {recoveryUID}", key)
		}
	})

	t.Run("the recovery block outlasts the failover timeout", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Failover = &apiv1.FailoverSpec{Timeout: "30m"}

		cfg := parse(t, cr)

		assert.Greater(t, cfg["RecoveryPeriodBlockSeconds"], float64(30*60),
			"orchestrator measures the block from the start of the recovery, so a shorter one lets a second recovery in while the hook runs")
	})

	t.Run("spec values are plumbed through", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Failover = &apiv1.FailoverSpec{
			Timeout:                  "30m",
			OnTimeout:                apiv1.FailoverPolicyForce,
			SwitchoverCatchUpTimeout: "2m",
		}

		cfg := parse(t, cr)
		hook := hookOf(t, cfg)

		assert.Contains(t, hook, "-timeout 30m0s")
		assert.Contains(t, hook, "-on-timeout ForceWithPossibleDataLoss")
		assert.EqualValues(t, 120, cfg["ReasonableMaintenanceReplicationLagSeconds"])
	})
}

func TestSwitchoverDowntimeOutlastsCatchUp(t *testing.T) {
	// A downtime that merely matched the catch-up wait would expire just as the
	// promotion starts, which is the case it is there to protect.
	for _, catchUp := range []time.Duration{time.Minute, 5 * time.Minute, 20 * time.Minute} {
		assert.Greater(t, SwitchoverDowntime(catchUp), int(catchUp.Seconds()))
	}

	// the value this replaced, for the default catch-up wait
	assert.Equal(t, 600, SwitchoverDowntime(5*time.Minute))
}
