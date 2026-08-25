package psbackup

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{
			name:     "zero bytes",
			bytes:    0,
			expected: "0B",
		},
		{
			name:     "bytes less than KB",
			bytes:    512,
			expected: "512B",
		},
		{
			name:     "exactly 1 KB",
			bytes:    1024,
			expected: "1.00KB",
		},
		{
			name:     "kilobytes",
			bytes:    78771, // ~76.92KB
			expected: "76.92KB",
		},
		{
			name:     "exactly 1 MB",
			bytes:    1024 * 1024,
			expected: "1.00MB",
		},
		{
			name:     "megabytes",
			bytes:    5 * 1024 * 1024,
			expected: "5.00MB",
		},
		{
			name:     "exactly 1 GB",
			bytes:    1024 * 1024 * 1024,
			expected: "1.00GB",
		},
		{
			name:     "gigabytes",
			bytes:    3 * 1024 * 1024 * 1024,
			expected: "3.00GB",
		},
		{
			name:     "exactly 1 TB",
			bytes:    1024 * 1024 * 1024 * 1024,
			expected: "1.00TB",
		},
		{
			name:     "terabytes",
			bytes:    2 * 1024 * 1024 * 1024 * 1024,
			expected: "2.00TB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			assert.Equal(t, tt.expected, result)
		})
	}
}
