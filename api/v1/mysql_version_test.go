package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMySQLVersionFromImage(t *testing.T) {
	tests := map[string]struct {
		image   string
		want    string
		wantErr error
	}{
		"full percona tag": {
			image: "percona/percona-server:8.4.6-6.1",
			want:  "8.4.6",
		},
		"major minor patch": {
			image: "percona/percona-server:8.0.43",
			want:  "8.0.43",
		},
		"major minor only": {
			image: "percona/percona-server:8.4",
			want:  "8.4.0",
		},
		"private registry with port": {
			image: "registry.example.com:5000/percona/percona-server:8.4.6-6.1",
			want:  "8.4.6",
		},
		"tag and digest": {
			image: "percona/percona-server:8.4.6-6.1@sha256:abc123",
			want:  "8.4.6",
		},
		"multi arch suffix": {
			image: "percona/percona-server:8.4.6-6.1-multi",
			want:  "8.4.6",
		},
		"operator build tag": {
			image: "perconalab/percona-server-mysql-operator:main-psmysql8.4",
			want:  "8.4.0",
		},
		"released operator build tag": {
			image: "percona/percona-server-mysql-operator:0.11.0-psmysql8.4.6",
			want:  "8.4.6",
		},
		"operator build tag without a version": {
			image:   "perconalab/percona-server-mysql-operator:main-psmysql",
			wantErr: ErrMySQLVersionUnknown,
		},
		"branch tag": {
			image:   "perconalab/percona-server-mysql-operator:main",
			wantErr: ErrMySQLVersionUnknown,
		},
		"latest tag": {
			image:   "percona/percona-server:latest",
			wantErr: ErrMySQLVersionUnknown,
		},
		"digest only": {
			image:   "percona/percona-server@sha256:abc123",
			wantErr: ErrMySQLVersionUnknown,
		},
		"no tag": {
			image:   "percona/percona-server",
			wantErr: ErrMySQLVersionUnknown,
		},
		"registry port and no tag": {
			image:   "registry.example.com:5000/percona/percona-server",
			wantErr: ErrMySQLVersionUnknown,
		},
		"empty image": {
			image:   "",
			wantErr: ErrMySQLVersionUnknown,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := MySQLVersionFromImage(tc.image)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestConfiguredMySQLVersion(t *testing.T) {
	tests := map[string]struct {
		version string
		image   string
		want    string
		wantErr error
	}{
		"spec version wins over the tag": {
			version: "8.0.46",
			image:   "percona/percona-server:8.4.6-6.1",
			want:    "8.0.46",
		},
		"spec version is used with an unparseable tag": {
			version: "8.4.6",
			image:   "percona/percona-server:main",
			want:    "8.4.6",
		},
		"spec version is trimmed": {
			version: "  8.4.6  ",
			image:   "percona/percona-server:main",
			want:    "8.4.6",
		},
		"falls back to the tag": {
			image: "percona/percona-server:8.4.6-6.1",
			want:  "8.4.6",
		},
		"blank spec version falls back to the tag": {
			version: "   ",
			image:   "percona/percona-server:8.4.6-6.1",
			want:    "8.4.6",
		},
		"neither is usable": {
			image:   "percona/percona-server:main",
			wantErr: ErrMySQLVersionUnknown,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &PerconaServerMySQL{
				Spec: PerconaServerMySQLSpec{
					MySQL: MySQLSpec{
						Version: tc.version,
						PodSpec: PodSpec{ContainerSpec: ContainerSpec{Image: tc.image}},
					},
				},
			}

			got, err := cr.ConfiguredMySQLVersion()
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
