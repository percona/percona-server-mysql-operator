package utils

import (
	"os"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetCloneTimeout(t *testing.T) {
	tests := map[string]struct {
		envValue       string
		expectedResult uint32
		expectedError  error
	}{
		"no environment variable set": {
			envValue:       "",
			expectedResult: 0,
		},
		"valid positive timeout": {
			envValue:       "300",
			expectedResult: 300,
		},
		"valid zero timeout (no timeout)": {
			envValue:       "0",
			expectedResult: 0,
		},
		"invalid negative timeout": {
			envValue:       "-1",
			expectedResult: 0,
			expectedError:  errors.New("BOOTSTRAP_CLONE_TIMEOUT should be a positive value"),
		},
		"invalid non-numeric timeout": {
			envValue:       "abc",
			expectedResult: 0,
			expectedError:  errors.New("failed to parse BOOTSTRAP_CLONE_TIMEOUT: strconv.Atoi: parsing \"abc\": invalid syntax"),
		},
		"invalid empty string": {
			envValue:       "",
			expectedResult: 0,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT")
			require.NoError(t, err)

			if tt.envValue != "" {
				_ = os.Setenv("BOOTSTRAP_CLONE_TIMEOUT", tt.envValue)
				defer func() {
					err := os.Unsetenv("BOOTSTRAP_CLONE_TIMEOUT")
					require.NoError(t, err)
				}()
			}

			result, err := GetCloneTimeout()

			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestGetSourceRetryCount(t *testing.T) {
	tests := map[string]struct {
		envValue       string
		expectedResult uint32
		expectedError  error
	}{
		"no environment variable set": {
			envValue:       "",
			expectedResult: 0,
		},
		"valid positive retry count": {
			envValue:       "5",
			expectedResult: 5,
		},
		"valid zero retry count": {
			envValue:       "0",
			expectedResult: 0,
		},
		"invalid negative retry count": {
			envValue:       "-1",
			expectedResult: 0,
			expectedError:  errors.New("ASYNC_SOURCE_RETRY_COUNT should be a positive value"),
		},
		"invalid non-numeric retry count": {
			envValue:       "abc",
			expectedResult: 0,
			expectedError:  errors.New("failed to parse ASYNC_SOURCE_RETRY_COUNT: strconv.Atoi: parsing \"abc\": invalid syntax"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := os.Unsetenv("ASYNC_SOURCE_RETRY_COUNT")
			require.NoError(t, err)

			if tt.envValue != "" {
				_ = os.Setenv("ASYNC_SOURCE_RETRY_COUNT", tt.envValue)
				defer func() {
					err := os.Unsetenv("ASYNC_SOURCE_RETRY_COUNT")
					require.NoError(t, err)
				}()
			}

			result, err := GetSourceRetryCount()

			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}
