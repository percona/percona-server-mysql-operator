package users

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDropUserStatements(t *testing.T) {
	tests := map[string]struct {
		name  string
		hosts []string
		want  []string
	}{
		"single host": {
			name:  "u",
			hosts: []string{"%"},
			want:  []string{"DROP USER IF EXISTS 'u'@'%'"},
		},
		"every host is dropped": {
			name:  "u",
			hosts: []string{"%", "10.0.0.1"},
			want: []string{
				"DROP USER IF EXISTS 'u'@'%'",
				"DROP USER IF EXISTS 'u'@'10.0.0.1'",
			},
		},
		"no hosts yields no statements": {
			name:  "u",
			hosts: nil,
			want:  nil,
		},
		"name is escaped": {
			name:  "o'brien",
			hosts: []string{"%"},
			want:  []string{"DROP USER IF EXISTS 'o''brien'@'%'"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, dropUserStatements(tt.name, tt.hosts))
		})
	}
}
