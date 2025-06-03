package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetTerminationGracePeriodSeconds(t *testing.T) {
	tests := map[string]struct {
		input    *int64
		expected int64
	}{
		"custom grace period": {
			input:    int64Ptr(20),
			expected: 20,
		},
		"nil grace period (default used)": {
			input:    nil,
			expected: 600,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			spec := PodSpec{
				TerminationGracePeriodSeconds: tc.input,
			}
			result := spec.GetTerminationGracePeriodSeconds()
			assert.Equal(t, tc.expected, *result)
		})
	}
}

func int64Ptr(i int64) *int64 {
	return &i
}
