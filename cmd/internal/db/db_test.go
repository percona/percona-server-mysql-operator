package db

import (
	"testing"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestDBParams_setDefaults(t *testing.T) {
	tests := []struct {
		name     string
		params   DBParams
		expected DBParams
	}{
		{
			name: "all defaults",
			params: DBParams{
				User: apiv1.UserOperator,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                33062,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
			},
		},
		{
			name: "custom values",
			params: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 300,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 300,
			},
		},
		{
			name: "zero port gets default",
			params: DBParams{
				User: apiv1.UserOperator,
				Port: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                33062,
				ReadTimeoutSeconds:  3600,
				CloneTimeoutSeconds: 3600,
			},
		},
		{
			name: "zero clone timeout gets default",
			params: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 0,
			},
			expected: DBParams{
				User:                apiv1.UserOperator,
				Port:                3306,
				ReadTimeoutSeconds:  30,
				CloneTimeoutSeconds: 3600,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.params.setDefaults()
			assert.Equal(t, tt.expected, tt.params)
		})
	}
}

func TestDBParams_DSN(t *testing.T) {
	params := DBParams{
		User:                apiv1.UserOperator,
		Pass:                "testpass",
		Host:                "localhost",
		Port:                3306,
		ReadTimeoutSeconds:  31,
		CloneTimeoutSeconds: 300,
	}

	dsn := params.DSN()

	assert.Contains(t, dsn, "operator")
	assert.Contains(t, dsn, "testpass")
	assert.Contains(t, dsn, "localhost:3306")
	assert.Contains(t, dsn, "performance_schema")
	assert.Contains(t, dsn, "readTimeout=31s")
	assert.Contains(t, dsn, "timeout=10s")
	assert.Contains(t, dsn, "writeTimeout=31s")
}
