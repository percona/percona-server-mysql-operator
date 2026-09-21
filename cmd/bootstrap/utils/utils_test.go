package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckClustersetRecovery(t *testing.T) {
	tests := map[string]struct {
		existingFiles   []string
		expectedRemoved []string
		expectedPresent []string
	}{
		"removes recovery files when clusterset recovery is requested": {
			existingFiles: []string{
				"no-bootstrap",
				"sleep-forever",
				"full-cluster-crash",
				"clusterset-recovery",
			},
			expectedRemoved: []string{
				"no-bootstrap",
				"sleep-forever",
				"full-cluster-crash",
				"clusterset-recovery",
			},
		},
		"keeps recovery files when clusterset recovery is not requested": {
			existingFiles: []string{
				"no-bootstrap",
				"sleep-forever",
				"full-cluster-crash",
			},
			expectedPresent: []string{
				"no-bootstrap",
				"sleep-forever",
				"full-cluster-crash",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			clusterSetRecoveryFile := filepath.Join(dir, "clusterset-recovery")
			recoveryFiles := []string{
				filepath.Join(dir, "no-bootstrap"),
				filepath.Join(dir, "sleep-forever"),
				filepath.Join(dir, "full-cluster-crash"),
				clusterSetRecoveryFile,
			}

			for _, file := range tt.existingFiles {
				require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte("x"), 0o600))
			}

			require.NoError(t, checkClustersetRecovery(clusterSetRecoveryFile, recoveryFiles))

			for _, file := range tt.expectedRemoved {
				_, err := os.Stat(filepath.Join(dir, file))
				assert.True(t, os.IsNotExist(err), "%s should be removed", file)
			}
			for _, file := range tt.expectedPresent {
				_, err := os.Stat(filepath.Join(dir, file))
				assert.NoError(t, err, "%s should remain", file)
			}
		})
	}
}

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

func TestGetCloneStallTimeout(t *testing.T) {
	tests := map[string]struct {
		set           bool
		envValue      string
		expectedValue uint32
		expectedSet   bool
		expectedError string
	}{
		"unset -> not present": {
			set:         false,
			expectedSet: false,
		},
		"valid positive": {
			set:           true,
			envValue:      "900",
			expectedValue: 900,
			expectedSet:   true,
		},
		"zero disables but is present": {
			set:           true,
			envValue:      "0",
			expectedValue: 0,
			expectedSet:   true,
		},
		"negative is an error": {
			set:           true,
			envValue:      "-1",
			expectedSet:   true,
			expectedError: "BOOTSTRAP_CLONE_STALL_TIMEOUT should be a non-negative value",
		},
		"non-numeric is an error": {
			set:           true,
			envValue:      "abc",
			expectedSet:   true,
			expectedError: "failed to parse BOOTSTRAP_CLONE_STALL_TIMEOUT: strconv.Atoi: parsing \"abc\": invalid syntax",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, os.Unsetenv("BOOTSTRAP_CLONE_STALL_TIMEOUT"))
			if tt.set {
				require.NoError(t, os.Setenv("BOOTSTRAP_CLONE_STALL_TIMEOUT", tt.envValue))
				defer func() { require.NoError(t, os.Unsetenv("BOOTSTRAP_CLONE_STALL_TIMEOUT")) }()
			}

			value, present, err := GetCloneStallTimeout()

			assert.Equal(t, tt.expectedSet, present)
			if tt.expectedError != "" {
				assert.EqualError(t, err, tt.expectedError)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedValue, value)
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

func TestGetSourceConnectRetry(t *testing.T) {
	tests := map[string]struct {
		envValue       string
		expectedResult uint32
		expectedError  error
	}{
		"no environment variable set": {
			envValue:       "",
			expectedResult: 0,
		},
		"valid positive connect retry": {
			envValue:       "60",
			expectedResult: 60,
		},
		"valid zero connect retry": {
			envValue:       "0",
			expectedResult: 0,
		},
		"invalid negative connect retry": {
			envValue:       "-1",
			expectedResult: 0,
			expectedError:  errors.New("ASYNC_SOURCE_CONNECT_RETRY should be a positive value"),
		},
		"invalid non-numeric connect retry": {
			envValue:       "abc",
			expectedResult: 0,
			expectedError:  errors.New("failed to parse ASYNC_SOURCE_CONNECT_RETRY: strconv.Atoi: parsing \"abc\": invalid syntax"),
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := os.Unsetenv("ASYNC_SOURCE_CONNECT_RETRY")
			require.NoError(t, err)

			if tt.envValue != "" {
				_ = os.Setenv("ASYNC_SOURCE_CONNECT_RETRY", tt.envValue)
				defer func() {
					err := os.Unsetenv("ASYNC_SOURCE_CONNECT_RETRY")
					require.NoError(t, err)
				}()
			}

			result, err := GetSourceConnectRetry()

			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}
