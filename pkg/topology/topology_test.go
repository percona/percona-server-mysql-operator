package topology

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestGroupReplicationClusterTypeGuard(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := map[string]struct {
		specType   apiv1.ClusterType
		statusType apiv1.ClusterType
		expectErr  bool
	}{
		"GR cluster": {
			specType: apiv1.ClusterTypeGR,
		},
		"async cluster": {
			specType:  apiv1.ClusterTypeAsync,
			expectErr: true,
		},
		"GR cluster with a pending switch to async": {
			specType:   apiv1.ClusterTypeAsync,
			statusType: apiv1.ClusterTypeGR,
		},
		"async cluster with a pending switch to GR": {
			specType:   apiv1.ClusterTypeGR,
			statusType: apiv1.ClusterTypeAsync,
			expectErr:  true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := &apiv1.PerconaServerMySQL{
				Name:      "test-cluster",
				Namespace: "test-namespace",
				Spec: apiv1.PerconaServerMySQLSpec{
					MySQL: apiv1.MySQLSpec{ClusterType: tt.specType},
				},
				Status: apiv1.PerconaServerMySQLStatus{ClusterType: tt.statusType},
			}

			// No pods exist, so a cluster that passes the guard returns an empty
			// topology without touching the database.
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()

			_, err := GroupReplication(t.Context(), cl, nil, cluster, "pass")
			if tt.expectErr {
				assert.ErrorContains(t, err, "cluster type is not group replication")
				return
			}
			assert.NoError(t, err)
		})
	}
}
