package config

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReader(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

func TestParseSection(t *testing.T) {
	tests := map[string]struct {
		input       string
		sectionName string
		wantErr     bool
		wantSection string
	}{
		"returns named section when present": {
			input: `
[mysqld]
innodb_buffer_pool_size=128M
`,
			sectionName: "mysqld",
			wantSection: "mysqld",
		},
		"falls back to default when named section absent": {
			input: `
key=value
`,
			sectionName: "mysqld",
			wantSection: "DEFAULT",
		},
		"empty file falls back to default section": {
			input:       ``,
			sectionName: "mysqld",
			wantSection: "DEFAULT",
		},
		"boolean key is accepted": {
			input: `
[mysqld]
skip-name-resolve
`,
			sectionName: "mysqld",
			wantSection: "mysqld",
		},
		"empty sectionName returns default section": {
			input:       "key=value\n",
			sectionName: "",
			wantSection: "DEFAULT",
		},
		"invalid ini content returns error": {
			input:       "[mysqld\nkey=val\n",
			sectionName: "mysqld",
			wantErr:     true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			section, err := ParseSection(newReader(tt.input), tt.sectionName)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, section)
			assert.Equal(t, tt.wantSection, section.Name())
		})
	}
}

func TestGetKeyValue(t *testing.T) {
	tests := map[string]struct {
		input     string
		option    string
		wantValue string
		wantErr   bool
	}{
		"returns value for exact key": {
			input:     "[mysqld]\ninnodb_buffer_pool_size=128M\n",
			option:    "innodb_buffer_pool_size",
			wantValue: "128M",
		},
		"returns value for loose_ prefixed key": {
			input:     "[mysqld]\nloose_group_replication_start_on_boot=off\n",
			option:    "group_replication_start_on_boot",
			wantValue: "off",
		},
		"exact key takes precedence over loose_ key": {
			input:     "[mysqld]\nmy_option=direct\nloose_my_option=loose\n",
			option:    "my_option",
			wantValue: "direct",
		},
		"returns empty string when key not found": {
			input:     "[mysqld]\nother_key=val\n",
			option:    "missing_key",
			wantValue: "",
		},
		"works with default section (no section header)": {
			input:     "my_option=hello\n",
			option:    "my_option",
			wantValue: "hello",
		},
		"returns empty string for boolean key (value-less)": {
			input:     "[mysqld]\nskip-name-resolve\n",
			option:    "skip-name-resolve",
			wantValue: "true",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			section, err := ParseSection(newReader(tt.input), "mysqld")
			require.NoError(t, err)

			val, err := GetKeyValue(section, tt.option)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, val)
		})
	}
}
