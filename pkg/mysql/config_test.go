package mysql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestGetConfig(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "config-ns"
	)

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
	}
	name := ConfigMapName(cr)

	// a nil configMap or secret means the object does not exist; a value is
	// stored under my.cnf
	tests := []struct {
		desc      string
		configMap *string
		secret    *string
		want      map[string]string
	}{
		{
			desc: "no configmap and no secret is an empty config",
		},
		{
			desc:      "reads keys from the configmap",
			configMap: new("[mysqld]\nmax_connections=100\n"),
			want:      map[string]string{"max_connections": "100"},
		},
		{
			desc:   "reads keys from the secret",
			secret: new("[mysqld]\nmax_connections=100\n"),
			want:   map[string]string{"max_connections": "100"},
		},
		{
			desc:      "merges keys from the configmap and the secret",
			configMap: new("[mysqld]\nmax_connections=100\n"),
			secret:    new("[mysqld]\nsql_mode=STRICT_TRANS_TABLES\n"),
			want: map[string]string{
				"max_connections": "100",
				"sql_mode":        "STRICT_TRANS_TABLES",
			},
		},
		{
			// The secret is appended after the configmap, and the last value of a
			// repeated key wins, so a secret can override the configmap.
			desc:      "secret overrides a key set in the configmap",
			configMap: new("[mysqld]\nmax_connections=100\n"),
			secret:    new("[mysqld]\nmax_connections=200\n"),
			want:      map[string]string{"max_connections": "200"},
		},
		{
			desc:      "returns only the mysqld section",
			configMap: new("[client]\nport=3307\n[mysqld]\nmax_connections=100\n"),
			want:      map[string]string{"max_connections": "100"},
		},
		{
			// Without a mysqld section anywhere, the unnamed section is returned
			// rather than nothing.
			desc:      "reads keys with no section header",
			configMap: new("max_connections=100\n"),
			want:      map[string]string{"max_connections": "100"},
		},
		{
			// The header-less configmap keys land above the secret's [mysqld]
			// header, so they end up in the unnamed section and are dropped.
			desc:      "header-less configmap keys are dropped when the secret declares mysqld",
			configMap: new("max_connections=100\n"),
			secret:    new("[mysqld]\nsql_mode=STRICT_TRANS_TABLES\n"),
			want:      map[string]string{"sql_mode": "STRICT_TRANS_TABLES"},
		},
		{
			// The reverse order absorbs them: the configmap's header precedes the
			// secret's keys.
			desc:      "header-less secret keys join the mysqld section of the configmap",
			configMap: new("[mysqld]\nmax_connections=100\n"),
			secret:    new("sql_mode=STRICT_TRANS_TABLES\n"),
			want: map[string]string{
				"max_connections": "100",
				"sql_mode":        "STRICT_TRANS_TABLES",
			},
		},
		{
			// A flag written without a value reads back as "true", which is what
			// the ini loader makes of a boolean key.
			desc:      "keeps a key that carries no value",
			configMap: new("[mysqld]\nskip-name-resolve\n"),
			want:      map[string]string{"skip-name-resolve": "true"},
		},
		{
			desc:      "an empty configmap value is an empty config",
			configMap: new(""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			objs := []client.Object{}
			if tt.configMap != nil {
				objs = append(objs, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					Data:       map[string]string{CustomConfigKey: *tt.configMap},
				})
			}
			if tt.secret != nil {
				objs = append(objs, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
					Data:       map[string][]byte{CustomConfigKey: []byte(*tt.secret)},
				})
			}

			cl := fake.NewClientBuilder().WithObjects(objs...).Build()

			section, err := GetConfig(context.Background(), cl, cr)
			require.NoError(t, err)

			want := tt.want
			if want == nil {
				want = map[string]string{}
			}
			assert.Equal(t, want, section.KeysHash())
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := map[string]struct {
		value string
		want  string
	}{
		"quotes a plain value":             {value: "utf8mb4", want: `'utf8mb4'`},
		"quotes an empty value":            {value: "", want: `''`},
		"doubles a single quote":           {value: "a'b", want: `'a''b'`},
		"doubles every single quote":       {value: "'a'b'", want: `'''a''b'''`},
		"doubles a backslash":              {value: `a\b`, want: `'a\\b'`},
		"leaves a double quote alone":      {value: `a"b`, want: `'a"b'`},
		"escapes a backslashed quote once": {value: `a\'b`, want: `'a\\''b'`},
		"contains a statement terminator":  {value: "a; DROP TABLE t", want: `'a; DROP TABLE t'`},
		"contains the injection payload": {
			value: `x' , GLOBAL super_read_only=0, @@dummy='`,
			want:  `'x'' , GLOBAL super_read_only=0, @@dummy='''`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, QuoteLiteral(tt.value))
		})
	}
}

func TestFormatConfigValue(t *testing.T) {
	tests := map[string]struct {
		value string
		want  string
	}{
		"passes integers through unquoted":       {value: "100", want: "100"},
		"passes floats through unquoted":         {value: "0.5", want: "0.5"},
		"passes negative numbers through":        {value: "-1", want: "-1"},
		"quotes strings":                         {value: "utf8mb4", want: "'utf8mb4'"},
		"quotes enum lists":                      {value: "STRICT_TRANS_TABLES,NO_ZERO_DATE", want: "'STRICT_TRANS_TABLES,NO_ZERO_DATE'"},
		"quotes an empty value":                  {value: "", want: "''"},
		"trims surrounding whitespace":           {value: "  100  ", want: "100"},
		"expands a kilobyte suffix":              {value: "16K", want: "16384"},
		"expands a megabyte suffix":              {value: "64M", want: "67108864"},
		"expands a gigabyte suffix":              {value: "1G", want: "1073741824"},
		"expands a terabyte suffix":              {value: "2T", want: "2199023255552"},
		"expands a lowercase suffix":             {value: "1g", want: "1073741824"},
		"reads a lowercase m as mega, not milli": {value: "100m", want: "104857600"},
		// Quantity saturates at MaxInt64 rather than reporting overflow, and it
		// accepts fractions that mysqld itself would reject. Both are recorded
		// here as known divergences, not as desired behaviour.
		"saturates an overflowing suffix":        {value: "9223372036854775807T", want: "9223372036854775807"},
		"accepts a suffixed non-integer":         {value: "1.5G", want: "1610612736"},
		"quotes a bare suffix":                   {value: "G", want: "'G'"},
		"quotes a word ending in a suffix":       {value: "PAGE", want: "'PAGE'"},
		"maps a bare boolean key to ON":          {value: "true", want: "ON"},
		"maps an explicit false to OFF":          {value: "false", want: "OFF"},
		"normalises on to uppercase":             {value: "on", want: "ON"},
		"normalises off to uppercase":            {value: "off", want: "OFF"},
		"leaves an enum resembling a bool alone": {value: "ON_PERMISSIVE", want: "'ON_PERMISSIVE'"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatConfigValue(tt.value))
		})
	}
}
