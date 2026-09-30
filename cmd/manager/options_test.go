package main

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

func TestConfigureOptions(t *testing.T) {
	for _, name := range []string{
		"PSO_LEADER_ELECTION_ENABLED",
		"MAX_CONCURRENT_RECONCILES",
	} {
		t.Setenv(name, "")
		require.NoError(t, os.Unsetenv(name))
	}
	t.Setenv("PSO_LEADER_ELECTION_LEASE_NAME", "")
	t.Setenv("PSO_LEADER_ELECTION_LEASE_DURATION", "30s")
	t.Setenv("PSO_LEADER_ELECTION_RENEW_DEADLINE", "20s")
	t.Setenv("PSO_LEADER_ELECTION_RETRY_PERIOD", "5s")
	t.Setenv("WATCH_NAMESPACE", "watch-a,watch-b")
	t.Setenv("OPERATOR_NAMESPACE", "operator-ns")

	options, err := configureOptions(":9090", ":9091", true)
	require.NoError(t, err)
	assert.Same(t, scheme, options.Scheme)
	assert.Equal(t, ":9090", options.Metrics.BindAddress)
	assert.Equal(t, ":9091", options.HealthProbeBindAddress)
	assert.NotNil(t, options.WebhookServer)
	assert.True(t, options.LeaderElection)
	assert.Equal(t, "operator-ns", options.LeaderElectionNamespace)
	assert.Equal(t, defaultElectionID, options.LeaderElectionID)
	assert.Equal(t, 30*time.Second, *options.LeaseDuration)
	assert.Equal(t, 20*time.Second, *options.RenewDeadline)
	assert.Equal(t, 5*time.Second, *options.RetryPeriod)
	assert.Equal(t, map[string]cache.Config{
		"watch-a":     {},
		"watch-b":     {},
		"operator-ns": {},
	}, options.Cache.DefaultNamespaces)
	assert.Equal(t, 1, options.Controller.GroupKindConcurrency["PerconaServerMySQL."+apiv1.GroupVersion.Group])
}

func TestConfigureOptionsRequiresWatchNamespace(t *testing.T) {
	t.Setenv("WATCH_NAMESPACE", "")
	require.NoError(t, os.Unsetenv("WATCH_NAMESPACE"))
	t.Setenv("OPERATOR_NAMESPACE", "operator-ns")

	_, err := configureOptions(":9090", ":9091", false)
	require.ErrorContains(t, err, "WATCH_NAMESPACE")
}

func TestConfigureOptionsAllowsEmptyNamespaces(t *testing.T) {
	t.Setenv("WATCH_NAMESPACE", "")
	t.Setenv("OPERATOR_NAMESPACE", "")

	options, err := configureOptions(":9090", ":9091", true)
	require.NoError(t, err)
	assert.Empty(t, options.LeaderElectionNamespace)
	assert.Nil(t, options.Cache.DefaultNamespaces)
}

func TestConfigureLeaderElection(t *testing.T) {
	enabled, disabled := true, false
	tests := []struct {
		name          string
		flagEnabled   bool
		envEnabled    *bool
		leaseName     string
		wantEnabled   bool
		wantID        string
		wantNamespace string
		wantError     string
	}{
		{
			name:   "flag disables election",
			wantID: defaultElectionID,
		},
		{
			name:        "environment disables election",
			flagEnabled: true,
			envEnabled:  &disabled,
			wantID:      defaultElectionID,
		},
		{
			name:          "environment enables election",
			envEnabled:    &enabled,
			wantEnabled:   true,
			wantID:        defaultElectionID,
			wantNamespace: "operator-ns",
		},
		{
			name:          "custom lease name",
			envEnabled:    &enabled,
			leaseName:     "custom.lease",
			wantEnabled:   true,
			wantID:        "custom.lease",
			wantNamespace: "operator-ns",
		},
		{
			name:       "invalid lease name",
			envEnabled: &enabled,
			leaseName:  "Invalid_Name",
			wantError:  "PSO_LEADER_ELECTION_LEASE_NAME",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := operatorOptions{
				operatorNamespace: "operator-ns",
				env: util.EnvConfig{
					LeaderElection:   tt.envEnabled,
					LeaderElectionID: tt.leaseName,
				},
			}

			err := config.configureLeaderElection(tt.flagEnabled)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEnabled, config.options.LeaderElection)
			assert.Equal(t, tt.wantID, config.options.LeaderElectionID)
			assert.Equal(t, tt.wantNamespace, config.options.LeaderElectionNamespace)
		})
	}
}

func TestConfigureGroupKindConcurrency(t *testing.T) {
	t.Setenv("WATCH_NAMESPACE", "")
	tests := map[string]struct {
		envValue      string
		expectedError string
		expectedVal   map[string]int
	}{
		"default concurrency when env not set": {
			envValue: "",
			expectedVal: map[string]int{
				"PerconaServerMySQL." + apiv1.GroupVersion.Group:           1,
				"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group:     1,
				"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group:    1,
				"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group: 1,
			},
		},
		"valid custom concurrency": {
			envValue: "5",
			expectedVal: map[string]int{
				"PerconaServerMySQL." + apiv1.GroupVersion.Group:           5,
				"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group:     5,
				"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group:    5,
				"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group: 5,
			},
		},
		"invalid non-integer value": {
			envValue: "invalid",
			expectedVal: map[string]int{
				"PerconaServerMySQL." + apiv1.GroupVersion.Group:           1,
				"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group:     1,
				"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group:    1,
				"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group: 1,
			},
			expectedError: "valid integer",
		},
		"zero value rejected": {
			envValue: "0",
			expectedVal: map[string]int{
				"PerconaServerMySQL." + apiv1.GroupVersion.Group:           1,
				"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group:     1,
				"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group:    1,
				"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group: 1,
			},
			expectedError: "positive number",
		},
		"negative value rejected": {
			envValue: "-1",
			expectedVal: map[string]int{
				"PerconaServerMySQL." + apiv1.GroupVersion.Group:           1,
				"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group:     1,
				"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group:    1,
				"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group: 1,
			},
			expectedError: "positive number",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MAX_CONCURRENT_RECONCILES", tt.envValue)
			if tt.envValue == "" {
				require.NoError(t, os.Unsetenv("MAX_CONCURRENT_RECONCILES"))
			}
			envs, parseErr := util.GetEnvConfig()
			if parseErr != nil {
				assert.ErrorContains(t, parseErr, "MAX_CONCURRENT_RECONCILES")
				return
			}

			options := ctrl.Options{
				Scheme: scheme,
				Metrics: metricsServer.Options{
					BindAddress: "bind-address",
				},
				HealthProbeBindAddress: "probe-address",
				LeaderElection:         true,
				LeaderElectionID:       "election-id",
			}

			optionsConfig := operatorOptions{options: options, env: envs}
			err := optionsConfig.configureGroupKindConcurrency()
			options = optionsConfig.options

			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, scheme, options.Scheme)
				assert.Equal(t, metricsServer.Options{
					BindAddress: "bind-address",
				}, options.Metrics)
				assert.Equal(t, "probe-address", options.HealthProbeBindAddress)
				assert.Equal(t, "election-id", options.LeaderElectionID)
				assert.True(t, options.LeaderElection)
			}
			assert.Equal(t, tt.expectedVal, options.Controller.GroupKindConcurrency)
		})
	}
}
