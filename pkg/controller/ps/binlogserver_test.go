package ps

import (
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
)

func TestBinlogServerSSLConfig(t *testing.T) {
	tests := map[string]struct {
		sslMode   string
		wantMode  string
		wantCerts bool
	}{
		"disabled mode skips certificates": {
			sslMode:   "disabled",
			wantMode:  "disabled",
			wantCerts: false,
		},
		"verify_identity includes certificates": {
			sslMode:   "verify_identity",
			wantMode:  "verify_identity",
			wantCerts: true,
		},
		"verify_ca includes certificates": {
			sslMode:   "verify_ca",
			wantMode:  "verify_ca",
			wantCerts: true,
		},
		"required includes certificates": {
			sslMode:   "required",
			wantMode:  "required",
			wantCerts: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := binlogServerSSLConfig(tt.sslMode)
			require.NotNil(t, got)

			assert.Equal(t, tt.wantMode, got.Mode)

			if tt.wantCerts {
				assert.Equal(t, path.Join(binlogserver.TLSMountPath, "ca.crt"), got.CA)
				assert.Equal(t, path.Join(binlogserver.TLSMountPath, "tls.crt"), got.Cert)
				assert.Equal(t, path.Join(binlogserver.TLSMountPath, "tls.key"), got.Key)
			} else {
				assert.Empty(t, got.CA)
				assert.Empty(t, got.Cert)
				assert.Empty(t, got.Key)
			}
		})
	}
}