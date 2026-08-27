package mysql

import (
	"context"
	"strings"
	"testing"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql/autoconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		desc       string
		autoConfig *string
		configMap  *string
		secret     *string
		want       map[string]string
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
			// Each fragment that doesn't open a section of its own is given a
			// [mysqld] header before the fragments are merged, so header-less
			// configmap keys survive the secret declaring the section.
			desc:      "header-less configmap keys join the mysqld section of the secret",
			configMap: new("max_connections=100\n"),
			secret:    new("[mysqld]\nsql_mode=STRICT_TRANS_TABLES\n"),
			want: map[string]string{
				"max_connections": "100",
				"sql_mode":        "STRICT_TRANS_TABLES",
			},
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
		{
			// The operator writes the auto-config without a section header.
			desc:       "reads keys from the auto-config configmap",
			autoConfig: new("\ninnodb_buffer_pool_size=3512016613\nmax_connections=442"),
			want: map[string]string{
				"innodb_buffer_pool_size": "3512016613",
				"max_connections":         "442",
			},
		},
		{
			desc:       "merges the auto-config with the user configuration",
			autoConfig: new("\ninnodb_buffer_pool_size=3512016613"),
			configMap:  new("[mysqld]\nsql_mode=STRICT_TRANS_TABLES\n"),
			want: map[string]string{
				"innodb_buffer_pool_size": "3512016613",
				"sql_mode":                "STRICT_TRANS_TABLES",
			},
		},
		{
			// The auto-config is merged first so the user always wins.
			desc:       "the configmap overrides a key set by the auto-config",
			autoConfig: new("\nmax_connections=442"),
			configMap:  new("[mysqld]\nmax_connections=100\n"),
			want:       map[string]string{"max_connections": "100"},
		},
		{
			desc:       "the secret overrides a key set by the auto-config",
			autoConfig: new("\nmax_connections=442"),
			secret:     new("[mysqld]\nmax_connections=200\n"),
			want:       map[string]string{"max_connections": "200"},
		},
		{
			// loose_x and x are the same variable to mysqld and to SET GLOBAL, so
			// only one may survive the merge; otherwise the dynamic-configuration
			// reconciler would apply both in map iteration order and let a
			// different value win on each pod.
			desc:       "the configmap overrides a loose key set by the auto-config",
			autoConfig: new("\nloose_group_replication_member_expel_timeout=5"),
			configMap:  new("[mysqld]\ngroup_replication_member_expel_timeout=99\n"),
			want:       map[string]string{"group_replication_member_expel_timeout": "99"},
		},
		{
			desc:       "a loose key in the configmap overrides the bare auto-config key",
			autoConfig: new("\nmax_connections=442"),
			configMap:  new("[mysqld]\nloose_max_connections=100\n"),
			want:       map[string]string{"loose_max_connections": "100"},
		},
		{
			desc:       "the secret overrides a loose key set by the auto-config",
			autoConfig: new("\nloose_group_replication_member_expel_timeout=5"),
			secret:     new("[mysqld]\ngroup_replication_member_expel_timeout=99\n"),
			want:       map[string]string{"group_replication_member_expel_timeout": "99"},
		},
		{
			// Nothing to collapse: the loose spelling is the only one present and
			// keeps its prefix, so an unknown variable stays skippable.
			desc:       "a loose key with no counterpart keeps its prefix",
			autoConfig: new("\nloose_group_replication_member_expel_timeout=5"),
			want:       map[string]string{"loose_group_replication_member_expel_timeout": "5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			objs := []client.Object{}
			if tt.autoConfig != nil {
				objs = append(objs, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: AutoConfigMapName(cr), Namespace: ns},
					Data:       map[string]string{CustomConfigKey: *tt.autoConfig},
				})
			}
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

func TestEffectiveResource(t *testing.T) {
	tests := map[string]struct {
		res  corev1.ResourceRequirements
		want string // empty => nil expected
	}{
		"limit only": {
			res: corev1.ResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Gi")},
			},
			want: "16Gi",
		},
		"request only": {
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
			},
			want: "1Gi",
		},
		"both set prefers limit": {
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("16Gi")},
			},
			want: "16Gi",
		},
		"neither set": {
			res:  corev1.ResourceRequirements{},
			want: "",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := EffectiveResource(tc.res, corev1.ResourceMemory)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.String())
		})
	}
}

func newAutoConfigCR(clusterType apiv1.ClusterType, loadType apiv1.AutoConfigLoadType, version, userConf string) *apiv1.PerconaServerMySQL {
	cr := &apiv1.PerconaServerMySQL{}
	cr.Spec.MySQL.ClusterType = clusterType
	cr.Spec.MySQL.AutoConfig.LoadType = loadType
	cr.Spec.MySQL.Configuration = userConf
	cr.Spec.MySQL.AutoConfig.Version = version
	return cr
}

func TestGetAutoConfigParams(t *testing.T) {
	cpu := resource.NewQuantity(4, resource.DecimalSI)    // 4 cores
	mem := resource.NewQuantity(8<<30, resource.BinarySI) // 8Gi
	zero := resource.NewQuantity(0, resource.DecimalSI)

	tests := map[string]struct {
		cr              *apiv1.PerconaServerMySQL
		cpu             *resource.Quantity
		memory          *resource.Quantity
		wantErrContains string
		// wantContains keys must appear in the output.
		wantContains []string
		// wantAbsent keys must NOT appear in the output.
		wantAbsent []string
	}{
		"group replication produces GR + innodb tuning": {
			cr:     newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", ""),
			cpu:    cpu,
			memory: mem,
			wantContains: []string{
				"innodb_buffer_pool_size=",
				"max_connections=",
				"loose_group_replication_",
			},
		},
		"async omits group replication settings": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeAsync, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", ""),
			cpu:          cpu,
			memory:       mem,
			wantContains: []string{"innodb_buffer_pool_size="},
			wantAbsent:   []string{"loose_group_replication_"},
		},
		"user configuration overrides calculated keys": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", "[mysqld]\ninnodb_buffer_pool_size=1G\nmax_connections=999"),
			cpu:          cpu,
			memory:       mem,
			wantAbsent:   []string{"innodb_buffer_pool_size=", "max_connections="},
			wantContains: []string{"innodb_redo_log_capacity="},
		},
		// The calculator emits this one as loose_group_replication_member_expel_timeout.
		// Both spellings name the same variable, so the user's must win outright
		// rather than the two ending up side by side.
		"user configuration overrides a calculated loose key": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", "[mysqld]\ngroup_replication_member_expel_timeout=99"),
			cpu:          cpu,
			memory:       mem,
			wantAbsent:   []string{"group_replication_member_expel_timeout="},
			wantContains: []string{"loose_group_replication_autorejoin_tries="},
		},
		"a loose user key overrides the calculated bare key": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", "[mysqld]\nloose_max_connections=999"),
			cpu:          cpu,
			memory:       mem,
			wantAbsent:   []string{"max_connections="},
			wantContains: []string{"innodb_buffer_pool_size="},
		},
		"missing cpu": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", ""),
			cpu:             zero,
			memory:          mem,
			wantErrContains: "cpu is required",
		},
		"missing memory": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8", ""),
			cpu:             cpu,
			memory:          zero,
			wantErrContains: "memory is required",
		},
		"empty mysql version": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "", ""),
			cpu:             cpu,
			memory:          mem,
			wantErrContains: "parse mysql version",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := GetAutoConfigParams(tc.cr, tc.cr.Spec.MySQL.AutoConfig.Version, tc.cpu, tc.memory)

			if tc.wantErrContains != "" {
				require.ErrorContains(t, err, tc.wantErrContains)
				return
			}
			require.NoError(t, err)

			for _, want := range tc.wantContains {
				assert.Truef(t, strings.Contains(got, want), "expected %q in output:\n%s", want, got)
			}
			for _, absent := range tc.wantAbsent {
				assert.Falsef(t, strings.Contains(got, absent), "did not expect %q in output:\n%s", absent, got)
			}
		})
	}
}

func TestParseMySQLVersion(t *testing.T) {
	tests := map[string]struct {
		in      string
		want    autoconfig.Version
		wantErr bool
	}{
		"full version":     {in: "8.4.8", want: autoconfig.Version{Major: 8, Minor: 4, Patch: 8}},
		"with suffix":      {in: "8.0.35-27", want: autoconfig.Version{Major: 8, Minor: 0, Patch: 35}},
		"major minor only": {in: "8.4", want: autoconfig.Version{Major: 8, Minor: 4, Patch: 0}},
		"empty":            {in: "", wantErr: true},
		"not a version":    {in: "abc", wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseMySQLVersion(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
