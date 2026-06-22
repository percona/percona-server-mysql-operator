package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestConfigureGroupKindConcurrency(t *testing.T) {
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
			if tt.envValue != "" {
				t.Setenv("MAX_CONCURRENT_RECONCILES", tt.envValue)
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

			err := configureGroupKindConcurrency(&options)

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
