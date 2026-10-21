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
		cr.Spec.Orchestrator.Configuration = `{"InstancePollSeconds": 3, "ReasonableReplicationLagSeconds": 20}`

		cfg := parse(t, cr)
		assert.EqualValues(t, 3, cfg["InstancePollSeconds"])
		assert.EqualValues(t, 20, cfg["ReasonableReplicationLagSeconds"])
	})

	t.Run("replication lag threshold defaults to a minute", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"

		cfg := parse(t, cr)
		assert.EqualValues(t, 60, cfg["ReasonableReplicationLagSeconds"])
	})

	t.Run("replication lag threshold stays user-tunable", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Configuration = `{"ReasonableReplicationLagSeconds": 20}`

		cfg := parse(t, cr)
		assert.EqualValues(t, 20, cfg["ReasonableReplicationLagSeconds"])
	})

	t.Run("replication lag threshold is left alone before 1.3.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"

		cfg := parse(t, cr)
		_, ok := cfg["ReasonableReplicationLagSeconds"]
		assert.False(t, ok)
	})

	t.Run("maintenance lag threshold is twice the replication lag threshold", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"

		cfg := parse(t, cr)
		assert.EqualValues(t, 120, cfg["ReasonableMaintenanceReplicationLagSeconds"])

		cr.Spec.Orchestrator.Configuration = `{"ReasonableReplicationLagSeconds": 30}`
		cfg = parse(t, cr)
		assert.EqualValues(t, 60, cfg["ReasonableMaintenanceReplicationLagSeconds"])
	})

	t.Run("maintenance lag threshold stays user-tunable", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Configuration = `{"ReasonableReplicationLagSeconds": 30, "ReasonableMaintenanceReplicationLagSeconds": 45}`

		cfg := parse(t, cr)
		assert.EqualValues(t, 45, cfg["ReasonableMaintenanceReplicationLagSeconds"])
	})

	t.Run("a non-numeric replication lag threshold is rejected", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Configuration = `{"ReasonableReplicationLagSeconds": "30"}`

		_, err := ConfigMapData(cr)
		assert.ErrorContains(t, err, "ReasonableReplicationLagSeconds")
	})

	t.Run("reserved keys cannot be overridden", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
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

		cr.Spec.Unsafe.OrchestratorSize = true
		cr.Spec.Orchestrator.Size = 1
		cfg = parse(t, cr)
		assert.Equal(t, []any{}, cfg["RaftNodes"])
		assert.EqualValues(t, true, cfg["RaftEnabledSingleNode"])
	})

	t.Run("operator-critical baked defaults cannot be overridden", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Configuration = `{
			"PreFailoverProcesses": ["echo pwned"],
			"PostUnsuccessfulFailoverProcesses": ["echo pwned"],
			"FailMasterPromotionOnLagMinutes": 30,
			"DelayMasterPromotionIfSQLThreadNotUpToDate": true,
			"ReasonableMaintenanceReplicationLagSeconds": 1200,
			"RecoveryPeriodBlockSeconds": 300,
			"UnseenInstanceForgetHours": 3,
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
		assert.EqualValues(t, 60*60+60*60, cfg["RecoveryPeriodBlockSeconds"])
		assert.EqualValues(t, 2, cfg["UnseenInstanceForgetHours"])

		// non-reserved keys (incl. the now-overridable ones) get through
		assert.EqualValues(t, 9, cfg["InstancePollSeconds"])
		assert.EqualValues(t, 1200, cfg["ReasonableMaintenanceReplicationLagSeconds"])
		assert.EqualValues(t, "evil", cfg["MySQLHostnameResolveMethod"])
		assert.EqualValues(t, "evil", cfg["HostnameResolveMethod"])
		assert.EqualValues(t, false, cfg["UseSuperReadOnly"])
	})

	t.Run("failover config is left alone before 1.3.0", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.2.0"
		cr.Spec.Orchestrator.Configuration = `{"FailMasterPromotionOnLagMinutes": 30, "RecoveryPeriodBlockSeconds": 300}`

		cfg := parse(t, cr)

		for _, k := range []string{
			"PreFailoverProcesses", "PostFailoverProcesses", "PostUnsuccessfulFailoverProcesses",
			"UnseenInstanceForgetHours", "ReasonableMaintenanceReplicationLagSeconds",
		} {
			_, ok := cfg[k]
			assert.Falsef(t, ok, "%s must not be in the ConfigMap", k)
		}
		assert.EqualValues(t, 30, cfg["FailMasterPromotionOnLagMinutes"])
		assert.EqualValues(t, 300, cfg["RecoveryPeriodBlockSeconds"])
	})
}

func TestBakedConfigDisablesPromotionLagVeto(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "orchestrator.conf.json"))
	require.NoError(t, err)

	cfg := map[string]any{}
	require.NoError(t, json.Unmarshal(data, &cfg))

	assert.EqualValues(t, 0, cfg["FailMasterPromotionOnLagMinutes"])
}

func TestSwitchoverCatchUp(t *testing.T) {
	tests := map[string]struct {
		version string
		config  string
		want    time.Duration
	}{
		"derived by default":                    {"1.3.0", "", 120 * time.Second},
		"follows the replication lag threshold": {"1.3.0", `{"ReasonableReplicationLagSeconds": 30}`, 60 * time.Second},
		"user value wins":                       {"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": 45}`, 45 * time.Second},
		"baked value before 1.3.0":              {"1.2.0", "", 300 * time.Second},
		"user value before 1.3.0":               {"1.2.0", `{"ReasonableMaintenanceReplicationLagSeconds": 45}`, 45 * time.Second},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.CRVersion = tt.version
			cr.Spec.Orchestrator.Configuration = tt.config

			got, err := SwitchoverCatchUp(cr)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSwitchoverCatchUpRejectsInvalidSeconds(t *testing.T) {
	const bounds = "must be a whole number of seconds between 1 and 9223372036"
	tests := map[string]struct {
		version string
		config  string
		want    string
	}{
		"fractional": {
			"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": 45.5}`,
			"ReasonableMaintenanceReplicationLagSeconds: " + bounds + ", got 45.5",
		},
		"zero": {
			"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": 0}`,
			"ReasonableMaintenanceReplicationLagSeconds: " + bounds + ", got 0",
		},
		"negative": {
			"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": -1}`,
			"ReasonableMaintenanceReplicationLagSeconds: " + bounds + ", got -1",
		},
		"overflowing": {
			"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": 1e19}`,
			"ReasonableMaintenanceReplicationLagSeconds: " + bounds + ", got 1e+19",
		},
		"not a number": {
			"1.3.0", `{"ReasonableMaintenanceReplicationLagSeconds": "45"}`,
			"ReasonableMaintenanceReplicationLagSeconds: must be a number, got 45",
		},
		"fractional replication lag": {
			"1.3.0", `{"ReasonableReplicationLagSeconds": 30.5}`,
			"ReasonableReplicationLagSeconds: " + bounds + ", got 30.5",
		},
		"zero replication lag": {
			"1.3.0", `{"ReasonableReplicationLagSeconds": 0}`,
			"ReasonableReplicationLagSeconds: " + bounds + ", got 0",
		},
		"derived lag overflows": {
			"1.3.0", `{"ReasonableReplicationLagSeconds": 9000000000}`,
			"derive ReasonableMaintenanceReplicationLagSeconds from ReasonableReplicationLagSeconds: " + bounds + ", got 18000000000",
		},
		"invalid user value before 1.3": {
			"1.2.0", `{"ReasonableMaintenanceReplicationLagSeconds": 0}`,
			"ReasonableMaintenanceReplicationLagSeconds: " + bounds + ", got 0",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.CRVersion = tt.version
			cr.Spec.Orchestrator.Configuration = tt.config

			_, err := SwitchoverCatchUp(cr)
			assert.EqualError(t, err, tt.want)
		})
	}

	t.Run("whole float accepted", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Configuration = `{"ReasonableMaintenanceReplicationLagSeconds": 45.0}`

		got, err := SwitchoverCatchUp(cr)
		require.NoError(t, err)
		assert.Equal(t, 45*time.Second, got)
	})
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
		cr.Spec.CRVersion = "1.3.0"

		cfg := parse(t, cr)
		hook := hookOf(t, cfg)

		assert.Contains(t, hook, "-timeout 1h0m0s")
		assert.Contains(t, hook, "-on-timeout Abort")
		// {command} is what tells a planned takeover from a primary that is
		// really gone; the hook must keep receiving it.
		assert.Contains(t, hook, "-command '{command}'")
		// {recoveryUID} is what the hook claims the source under and what the
		// post hooks release and acknowledge.
		assert.Contains(t, hook, "-uid {recoveryUID}")
	})

	t.Run("every post hook finishes the recovery", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"

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

	// finish lets orchestrator retry only the recoveries that gave up without
	// promoting, which it tells apart by the failure type
	t.Run("only the unsuccessful post hook passes the failure type to finish", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"

		cfg := parse(t, cr)

		finishOf := func(key string) string {
			processes, ok := cfg[key].([]any)
			require.True(t, ok, "%s must be a list", key)
			last, ok := processes[len(processes)-1].(string)
			require.True(t, ok)
			return last
		}

		assert.Contains(t, finishOf("PostUnsuccessfulFailoverProcesses"), "-failure-type '{failureType}'")
		assert.NotContains(t, finishOf("PostFailoverProcesses"), "-failure-type")
	})

	t.Run("the recovery block outlasts the failover timeout", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Failover = &apiv1.FailoverSpec{Timeout: "30m"}

		cfg := parse(t, cr)

		assert.Greater(t, cfg["RecoveryPeriodBlockSeconds"], float64(30*60),
			"orchestrator measures the block from the start of the recovery, so a shorter one lets a second recovery in while the hook runs")
	})

	t.Run("the dead primary is never forgotten mid-failover", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Failover = &apiv1.FailoverSpec{Timeout: "90m"}

		cfg := parse(t, cr)

		forget := time.Duration(cfg["UnseenInstanceForgetHours"].(float64)) * time.Hour
		assert.GreaterOrEqual(t, forget, 90*time.Minute+time.Hour,
			"orchestrator forgets the dead primary after this long, and with it the DeadMaster analysis and the forced takeover")
		assert.EqualValues(t, 3, cfg["UnseenInstanceForgetHours"], "orchestrator takes whole hours, so the value rounds up")
	})

	t.Run("spec values are plumbed through", func(t *testing.T) {
		cr := &apiv1.PerconaServerMySQL{}
		cr.Spec.CRVersion = "1.3.0"
		cr.Spec.Orchestrator.Failover = &apiv1.FailoverSpec{
			Timeout:   "30m",
			OnTimeout: apiv1.FailoverPolicyForce,
		}

		cfg := parse(t, cr)
		hook := hookOf(t, cfg)

		assert.Contains(t, hook, "-timeout 30m0s")
		assert.Contains(t, hook, "-on-timeout ForceWithPossibleDataLoss")
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
