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

func mysqldSection(t *testing.T, raw string) *Section {
	t.Helper()

	section, err := ParseSection(newReader(raw), "mysqld")
	require.NoError(t, err)
	return &Section{*section}
}

func emptySection(t *testing.T) *Section {
	t.Helper()

	return mysqldSection(t, "[mysqld]\n")
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

func TestChanged(t *testing.T) {
	tests := map[string]struct {
		a    *Section
		b    *Section
		want []string
	}{
		"returns keys whose value differs": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=new\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=old\n"),
			want: []string{"key_one"},
		},
		"skips keys with identical values": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			want: []string{},
		},
		"skips keys missing from b": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			want: []string{},
		},
		"skips keys missing from a": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			want: []string{},
		},
		"reports only the changed keys of a mixed section": {
			a:    mysqldSection(t, "[mysqld]\nsame=1\nchanged=new\nonly_in_a=3\n"),
			b:    mysqldSection(t, "[mysqld]\nsame=1\nchanged=old\nonly_in_b=4\n"),
			want: []string{"changed"},
		},
		"reports every changed key": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=new\nkey_two=2\nkey_three=new\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=old\nkey_two=2\nkey_three=old\n"),
			want: []string{"key_one", "key_three"},
		},
		"reports boolean key when its value differs": {
			a:    mysqldSection(t, "[mysqld]\nskip-name-resolve\n"),
			b:    mysqldSection(t, "[mysqld]\nskip-name-resolve=off\n"),
			want: []string{"skip-name-resolve"},
		},
		"returns empty when a has no keys": {
			a:    emptySection(t),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			want: []string{},
		},
		"returns empty when b has no keys": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			b:    emptySection(t),
			want: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, tt.a.Changed(*tt.b))
		})
	}
}

func TestSubtract(t *testing.T) {
	tests := map[string]struct {
		a    *Section
		b    *Section
		want []string
	}{
		"returns keys of a that are missing from b": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			want: []string{"key_two"},
		},
		"skips keys present in b even when the value differs": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=new\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=old\n"),
			want: []string{},
		},
		"returns empty when a is a subset of b": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			want: []string{},
		},
		"returns boolean keys": {
			a:    mysqldSection(t, "[mysqld]\nskip-name-resolve\n"),
			b:    emptySection(t),
			want: []string{"skip-name-resolve"},
		},
		"returns all keys of a when b has no keys": {
			a:    mysqldSection(t, "[mysqld]\nkey_one=1\nkey_two=2\n"),
			b:    emptySection(t),
			want: []string{"key_one", "key_two"},
		},
		"returns empty when a has no keys": {
			a:    emptySection(t),
			b:    mysqldSection(t, "[mysqld]\nkey_one=1\n"),
			want: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.ElementsMatch(t, tt.want, tt.a.Subtract(*tt.b))
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
