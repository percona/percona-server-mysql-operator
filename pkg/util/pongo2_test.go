package util

import (
	"testing"

	"github.com/flosch/pongo2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSandboxedTemplateSet(t *testing.T) {
	tests := map[string]struct {
		template    string
		context     pongo2.Context
		expected    string
		expectedErr string
	}{
		"safe variable interpolation": {
			template: "pool_size={{ containerMemoryLimit }}",
			context:  pongo2.Context{"containerMemoryLimit": int64(1073741824)},
			expected: "pool_size=1073741824",
		},
		"plain text without variables": {
			template: "innodb_buffer_pool_size=128M",
			context:  pongo2.Context{},
			expected: "innodb_buffer_pool_size=128M",
		},
		"banned tag ssi": {
			template:    `{% ssi "/var/run/secrets/kubernetes.io/serviceaccount/token" %}`,
			expectedErr: "is not allowed",
		},
		"banned tag include": {
			template:    `{% include "some_file.html" %}`,
			expectedErr: "is not allowed",
		},
		"banned tag import": {
			template:    `{% import "macros.html" %}`,
			expectedErr: "is not allowed",
		},
		"banned tag extends": {
			template:    `{% extends "base.html" %}`,
			expectedErr: "is not allowed",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			set, err := SandboxedTemplateSet()
			require.NoError(t, err)

			tmpl, err := set.FromString(tc.template)
			if tc.expectedErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedErr)
				return
			}
			require.NoError(t, err)

			result, err := tmpl.Execute(tc.context)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result)
		})
	}
}