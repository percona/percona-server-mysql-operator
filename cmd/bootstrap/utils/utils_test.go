package utils

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCloneTimeout(t *testing.T) {
	tests := []struct {
		name           string
		envValue       string
		expectedResult uint32
		expectedError  bool
	}{
		{
			name:           "no environment variable set",
			envValue:       "",
			expectedResult: 0,
			expectedError:  false,
		},
		{
			name:           "valid positive timeout",
			envValue:       "300",
			expectedResult: 300,
			expectedError:  false,
		},
		{
			name:           "valid zero timeout (no timeout)",
			envValue:       "0",
			expectedResult: 0,
			expectedError:  false,
		},
		{
			name:           "invalid negative timeout",
			envValue:       "-1",
			expectedResult: 0,
			expectedError:  true,
		},
		{
			name:           "invalid non-numeric timeout",
			envValue:       "abc",
			expectedResult: 0,
			expectedError:  true,
		},
		{
			name:           "invalid empty string",
			envValue:       "",
			expectedResult: 0,
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clean up any existing environment variable
			_ = os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT")

			// Set the environment variable if needed
			if tt.envValue != "" {
				_ = os.Setenv("BOOTSTRAP_CLONE_TIMEOUT", tt.envValue)
				defer func() { _ = os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT") }()
			}

			result, err := GetCloneTimeout()

			if tt.expectedError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "BOOTSTRAP_CLONE_TIMEOUT")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestGetCloneTimeout_EnvironmentVariable(t *testing.T) {
	// Test that the function reads from the correct environment variable
	_ = os.Setenv("BOOTSTRAP_CLONE_TIMEOUT", "600")
	defer func() { _ = os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT") }()

	result, err := GetCloneTimeout()
	require.NoError(t, err)
	assert.Equal(t, uint32(600), result)
}

func TestGetCloneTimeout_ZeroTimeout(t *testing.T) {
	// Test that zero timeout is valid (disables timeout)
	_ = os.Setenv("BOOTSTRAP_CLONE_TIMEOUT", "0")
	defer func() { _ = os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT") }()

	result, err := GetCloneTimeout()
	require.NoError(t, err)
	assert.Equal(t, uint32(0), result)
}
