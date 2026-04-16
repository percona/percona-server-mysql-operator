package ps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEndpointURL(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantProtocol string
		wantHost     string
		wantErr      string
	}{
		{
			name:         "https with host and port",
			input:        "https://minio-service:9000",
			wantProtocol: "https",
			wantHost:     "minio-service:9000",
		},
		{
			name:         "s3 scheme",
			input:        "s3://s3.amazonaws.com",
			wantProtocol: "s3",
			wantHost:     "s3.amazonaws.com",
		},
		{
			name:         "http scheme",
			input:        "http://localhost:9000",
			wantProtocol: "http",
			wantHost:     "localhost:9000",
		},
		{
			name:         "trailing slash is stripped",
			input:        "https://minio:9000/",
			wantProtocol: "https",
			wantHost:     "minio:9000",
		},
		{
			name:         "path after host is stripped",
			input:        "https://host/path/to/something",
			wantProtocol: "https",
			wantHost:     "host",
		},
		{
			name:         "host with port and path is stripped",
			input:        "https://minio:9000/bucket/prefix",
			wantProtocol: "https",
			wantHost:     "minio:9000",
		},
		{
			name:    "no scheme",
			input:   "minio-service:9000",
			wantErr: "must include protocol and host",
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: "must include protocol and host",
		},
		{
			name:    "scheme only",
			input:   "https://",
			wantErr: "must include protocol and host",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			protocol, host, err := parseEndpointURL(tc.input)

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantProtocol, protocol)
			assert.Equal(t, tc.wantHost, host)
		})
	}
}
