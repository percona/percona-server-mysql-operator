package autoconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculate(t *testing.T) {
	tests := map[string]struct {
		req             Request
		wantErr         error  // matched with ErrorIs
		wantErrContains string // matched with ErrorContains
	}{
		"group replication": {
			req: Request{
				DBType:      DBTypeGroupReplication,
				CPU:         4000,
				Memory:      "8G",
				Connections: 3000,
				Version:     Version{Major: 8, Minor: 4, Patch: 8},
				LoadType:    LoadTypeSomeWrites,
			},
		},
		"async": {
			req: Request{
				DBType:      DBTypeAsync,
				CPU:         2500,
				Memory:      "4G",
				Connections: 1000,
				Version:     Version{Major: 8, Minor: 4, Patch: 8},
				LoadType:    LoadTypeMostlyReads,
			},
		},
		"defaults applied when dbtype and loadtype omitted": {
			req: Request{
				CPU:     4000,
				Memory:  "8G",
				Version: Version{Major: 8, Minor: 4, Patch: 8},
			},
		},
		"missing memory": {
			req:     Request{CPU: 4000},
			wantErr: ErrMemoryRequired,
		},
		"missing cpu": {
			req:     Request{Memory: "8G"},
			wantErr: ErrCPURequired,
		},
		"invalid memory string": {
			req:             Request{CPU: 4000, Memory: "not-a-size"},
			wantErrContains: "convert memory",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			res, err := Calculate(tc.req)

			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, res)
				return
			}
			if tc.wantErrContains != "" {
				require.ErrorContains(t, err, tc.wantErrContains)
				assert.Nil(t, res)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, res)

			cnf, err := res.MySQLdConfig()
			require.NoError(t, err)
			assert.Contains(t, cnf, "innodb_buffer_pool_size")

			params, err := res.MySQLdParams()
			require.NoError(t, err)
			assert.Contains(t, params, "innodb_buffer_pool_size")
			assert.NotContains(t, params, "timeoutSeconds")
		})
	}
}
