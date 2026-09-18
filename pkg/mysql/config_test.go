package mysql

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/percona/percona-server-mysql-operator/pkg/mysql/autoconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
)

func TestGetConfig(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "config-ns"
	)

	cr := &apiv1.PerconaServerMySQL{
		Name: crName, Namespace: ns,
	}
	name := ConfigMapName(cr)

	// a nil configMap or secret means the object does not exist; a value is
	// stored under my.cnf
	tests := []struct {
		desc       string
		autoConfig *string
		specConfig string
		memory     string
		configMap  *string
		secret     *string
		want       map[string]string
		wantErrMsg string
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
		{
			desc:       "the spec configuration is preferred over the generated configmap",
			specConfig: "max_connections=250",
			configMap:  new("[mysqld]\nmax_connections=100\n"),
			want:       map[string]string{"max_connections": "250"},
		},
		{
			desc:       "a variable moved from the auto-config to the spec is still present",
			autoConfig: new("\ninnodb_buffer_pool_size=4294967296"),
			specConfig: "max_connections=250",
			want: map[string]string{
				"innodb_buffer_pool_size": "4294967296",
				"max_connections":         "250",
			},
		},
		{
			desc:       "the secret still overrides the spec configuration",
			specConfig: "max_connections=250",
			secret:     new("[mysqld]\nmax_connections=99\n"),
			want:       map[string]string{"max_connections": "99"},
		},
		{
			desc:       "a template in the spec configuration is rendered against the memory limit",
			specConfig: "max_heap_table_size={{ containerMemoryLimit }}",
			memory:     "1Gi",
			want:       map[string]string{"max_heap_table_size": "1073741824"},
		},
		{
			desc:       "a template without a memory limit is an error",
			specConfig: "max_heap_table_size={{ containerMemoryLimit }}",
			wantErrMsg: "resources.limits[memory] or resources.requests[memory] should be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			cr := cr.DeepCopy()
			cr.Spec.MySQL.Configuration = tt.specConfig
			if tt.memory != "" {
				cr.Spec.MySQL.Resources = corev1.ResourceRequirements{
					Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(tt.memory)},
				}
			}

			objs := []client.Object{}
			autoConf := ""
			if tt.autoConfig != nil {
				autoConf = *tt.autoConfig
			}
			if tt.configMap != nil {
				objs = append(objs, &corev1.ConfigMap{
					Name: name, Namespace: ns,
					Data: map[string]string{CustomConfigKey: *tt.configMap},
				})
			}
			if tt.secret != nil {
				objs = append(objs, &corev1.Secret{
					Name: name, Namespace: ns,
					Data: map[string][]byte{CustomConfigKey: []byte(*tt.secret)},
				})
			}

			cl := fake.NewClientBuilder().WithObjects(objs...).Build()

			section, err := GetConfig(t.Context(), cl, cr, autoConf)
			if tt.wantErrMsg != "" {
				require.ErrorContains(t, err, tt.wantErrMsg)
				assert.Equal(t, config.EmptySection, section)
				return
			}
			require.NoError(t, err)

			want := tt.want
			if want == nil {
				want = map[string]string{}
			}
			assert.Equal(t, want, section.KeysHash())
		})
	}
}

func TestHasUserConfig(t *testing.T) {
	const (
		crName = "cluster1"
		ns     = "config-ns"
	)

	newCR := func(configuration string) *apiv1.PerconaServerMySQL {
		cr := &apiv1.PerconaServerMySQL{
			Name: crName, Namespace: ns,
		}
		cr.Spec.MySQL.Configuration = configuration
		return cr
	}

	errBoom := errors.New("boom")

	failOn := func(want client.Object) interceptor.Funcs {
		return interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if reflect.TypeOf(obj) == reflect.TypeOf(want) {
					return errBoom
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}
	}

	tests := map[string]struct {
		configuration string
		configMap     *string
		secret        *string
		intercept     interceptor.Funcs

		want       bool
		wantErrMsg string
	}{
		"nothing set": {
			want: false,
		},
		"mysql.configuration is set": {
			configuration: "[mysqld]\nmax_connections=100\n",
			want:          true,
		},
		"mysql.configuration holds only whitespace": {
			configuration: "  \n\t\n",
			want:          false,
		},
		"the configmap carries a configuration": {
			configMap: new("[mysqld]\nmax_connections=100\n"),
			want:      true,
		},
		"the secret carries a configuration": {
			secret: new("[mysqld]\nmax_connections=100\n"),
			want:   true,
		},
		"an empty configmap does not count": {
			configMap: new(""),
			want:      false,
		},
		"a whitespace-only secret does not count": {
			secret: new("\n \n"),
			want:   false,
		},
		"reading the configmap fails": {
			intercept:  failOn(&corev1.ConfigMap{}),
			wantErrMsg: "get configmap",
		},
		"reading the secret fails": {
			intercept:  failOn(&corev1.Secret{}),
			wantErrMsg: "get secret",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := newCR(tc.configuration)
			cmName := ConfigMapName(cr)

			objs := []client.Object{}
			if tc.configMap != nil {
				objs = append(objs, &corev1.ConfigMap{
					Name: cmName, Namespace: ns,
					Data: map[string]string{CustomConfigKey: *tc.configMap},
				})
			}
			if tc.secret != nil {
				objs = append(objs, &corev1.Secret{
					Name: cmName, Namespace: ns,
					Data: map[string][]byte{CustomConfigKey: []byte(*tc.secret)},
				})
			}

			cl := fake.NewClientBuilder().WithObjects(objs...).WithInterceptorFuncs(tc.intercept).Build()

			got, err := HasUserConfig(t.Context(), cl, cr)
			if tc.wantErrMsg != "" {
				require.ErrorIs(t, err, errBoom)
				assert.ErrorContains(t, err, tc.wantErrMsg)
				assert.False(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
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

func newAutoConfigCR(clusterType apiv1.ClusterType, loadType apiv1.AutoConfigLoadType, version string) *apiv1.PerconaServerMySQL {
	cr := &apiv1.PerconaServerMySQL{}
	cr.Spec.MySQL.ClusterType = clusterType
	cr.Spec.MySQL.AutoConfig.LoadType = loadType
	cr.Spec.MySQL.AutoConfig.Version = version
	return cr
}

// withDataVolume declares a PVC of the given size for the MySQL data volume.
func withDataVolume(cr *apiv1.PerconaServerMySQL, size string) *apiv1.PerconaServerMySQL {
	cr.Spec.MySQL.VolumeSpec = &apiv1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(size)},
			},
		},
	}
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
			cr:     newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"),
			cpu:    cpu,
			memory: mem,
			wantContains: []string{
				"innodb_buffer_pool_size=",
				"max_connections=",
				"loose_group_replication_",
			},
		},
		"async omits group replication settings": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeAsync, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"),
			cpu:          cpu,
			memory:       mem,
			wantContains: []string{"innodb_buffer_pool_size="},
			wantAbsent:   []string{"loose_group_replication_"},
		},
		// The redo log is preallocated in full at startup, and a node joining by
		// clone needs free space for the donor's estimate on top of its own, so
		// a redo log over a quarter of the volume is rejected rather than
		// resized. At 8Gi of memory the calculator asks for ~4.4Gi.
		"a data volume too small for the calculated redo log is rejected": {
			cr:              withDataVolume(newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"), "2Gi"),
			cpu:             cpu,
			memory:          mem,
			wantErrContains: "data volume is too small",
		},
		"a data volume with room keeps the calculated redo log": {
			cr:           withDataVolume(newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"), "32Gi"),
			cpu:          cpu,
			memory:       mem,
			wantContains: []string{"innodb_redo_log_capacity=4714155900"},
		},
		// emptyDir and hostPath have no declared size to check against.
		"no persistent volume keeps the calculated redo log": {
			cr:           newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"),
			cpu:          cpu,
			memory:       mem,
			wantContains: []string{"innodb_redo_log_capacity=4714155900"},
		},
		"missing cpu": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"),
			cpu:             zero,
			memory:          mem,
			wantErrContains: "cpu is required",
		},
		"missing memory": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, "8.4.8"),
			cpu:             cpu,
			memory:          zero,
			wantErrContains: "memory is required",
		},
		"empty mysql version": {
			cr:              newAutoConfigCR(apiv1.ClusterTypeGR, apiv1.AutoConfigLoadTypeSomeWrites, ""),
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
		in         string
		want       autoconfig.Version
		wantErrMsg string
	}{
		"full version":     {in: "8.4.8", want: autoconfig.Version{Major: 8, Minor: 4, Patch: 8}},
		"with suffix":      {in: "8.0.35-27", want: autoconfig.Version{Major: 8, Minor: 0, Patch: 35}},
		"major minor only": {in: "8.4", want: autoconfig.Version{Major: 8, Minor: 4, Patch: 0}},
		"empty":            {in: "", wantErrMsg: "version is empty"},
		"whitespace only":  {in: "   ", wantErrMsg: "version is empty"},
		"not a version":    {in: "abc", wantErrMsg: "malformed version: abc"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseMySQLVersion(tc.in)
			if tc.wantErrMsg != "" {
				require.EqualError(t, err, tc.wantErrMsg)
				assert.Equal(t, autoconfig.Version{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestDataVolumeSize(t *testing.T) {
	tests := map[string]struct {
		volumeSpec *apiv1.VolumeSpec
		want       int64
	}{
		"no volume spec": {
			volumeSpec: nil,
			want:       0,
		},
		"emptyDir": {
			volumeSpec: &apiv1.VolumeSpec{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			want:       0,
		},
		"hostPath": {
			volumeSpec: &apiv1.VolumeSpec{HostPath: &corev1.HostPathVolumeSource{Path: "/data"}},
			want:       0,
		},
		"request only": {
			volumeSpec: pvcVolumeSpec(corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
			}),
			want: 2 * 1024 * 1024 * 1024,
		},
		"limit only": {
			volumeSpec: pvcVolumeSpec(corev1.VolumeResourceRequirements{
				Limits: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("3Gi")},
			}),
			want: 3 * 1024 * 1024 * 1024,
		},
		"both set prefers request": {
			volumeSpec: pvcVolumeSpec(corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")},
				Limits:   corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			}),
			want: 2 * 1024 * 1024 * 1024,
		},
		"pvc without storage resources": {
			volumeSpec: pvcVolumeSpec(corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			}),
			want: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.MySQL.VolumeSpec = tc.volumeSpec

			assert.Equal(t, tc.want, dataVolumeSize(cr))
		})
	}
}

func pvcVolumeSpec(res corev1.VolumeResourceRequirements) *apiv1.VolumeSpec {
	return &apiv1.VolumeSpec{
		PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{Resources: res},
	}
}

func TestCheckRedoLogFits(t *testing.T) {
	tests := map[string]struct {
		volume  string // empty => no persistent volume
		params  map[string]string
		wantErr error
	}{
		"no persistent volume is not checked": {
			params: map[string]string{"innodb_redo_log_capacity": "4294967296"},
		},
		"no redo log in the calculated params": {
			volume: "8Gi",
			params: map[string]string{"innodb_buffer_pool_size": "1073741824"},
		},
		"redo log within budget": {
			volume: "8Gi",
			params: map[string]string{"innodb_redo_log_capacity": "1073741824"},
		},
		"redo log exactly at budget": {
			volume: "8Gi",
			params: map[string]string{"innodb_redo_log_capacity": "2147483648"},
		},
		"redo log over budget is rejected": {
			volume:  "8Gi",
			params:  map[string]string{"innodb_redo_log_capacity": "2147483649"},
			wantErr: ErrInsufficientStorage,
		},
		"a redo log that fits the volume but not the budget is rejected": {
			volume:  "8Gi",
			params:  map[string]string{"innodb_redo_log_capacity": "4294967296"},
			wantErr: ErrInsufficientStorage,
		},
		"pre-8.4 spelling is totalled across the file count": {
			volume: "8Gi",
			params: map[string]string{
				"innodb_log_file_size":      "1073741824",
				"innodb_log_files_in_group": "2",
			},
		},
		"pre-8.4 spelling over budget is rejected": {
			volume: "8Gi",
			params: map[string]string{
				"innodb_log_file_size":      "2147483648",
				"innodb_log_files_in_group": "2",
			},
			wantErr: ErrInsufficientStorage,
		},
		"pre-8.4 spelling without a file count assumes a single file": {
			volume: "8Gi",
			params: map[string]string{"innodb_log_file_size": "2147483648"},
		},
		"an unparseable redo log value is an error": {
			volume:  "8Gi",
			params:  map[string]string{"innodb_redo_log_capacity": "big"},
			wantErr: strconv.ErrSyntax,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			if tc.volume != "" {
				withDataVolume(cr, tc.volume)
			}
			before := maps.Clone(tc.params)

			err := checkRedoLogFits(cr, tc.params)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, before, tc.params, "the calculated params must not be rewritten")
		})
	}
}

func TestGetAutoConfigParamsLoadType(t *testing.T) {
	cpu := resource.NewQuantity(4, resource.DecimalSI)
	mem := resource.NewQuantity(8<<30, resource.BinarySI)

	loadTypes := []apiv1.AutoConfigLoadType{
		apiv1.AutoConfigLoadTypeMostlyReads,
		apiv1.AutoConfigLoadTypeSomeWrites,
		apiv1.AutoConfigLoadTypeEqualReadsWrites,
		apiv1.AutoConfigLoadTypeHeavyWrites,
	}

	got := make(map[apiv1.AutoConfigLoadType]string, len(loadTypes))
	for _, lt := range loadTypes {
		params, err := GetAutoConfigParams(newAutoConfigCR(apiv1.ClusterTypeGR, lt, "8.4.8"), "8.4.8", cpu, mem)
		require.NoErrorf(t, err, "load type %q", lt)
		got[lt] = params
	}

	for i, a := range loadTypes {
		for _, b := range loadTypes[i+1:] {
			assert.NotEqualf(t, got[a], got[b], "%q and %q produced the same config", a, b)
		}
	}

	// An unset load type falls back to someWrites.
	unset, err := GetAutoConfigParams(newAutoConfigCR(apiv1.ClusterTypeGR, "", "8.4.8"), "8.4.8", cpu, mem)
	require.NoError(t, err)
	assert.Equal(t, got[apiv1.AutoConfigLoadTypeSomeWrites], unset)
}

func TestConfigurableGetConfiguration(t *testing.T) {
	tests := map[string]struct {
		configuration string
		want          string
	}{
		"empty": {},
		"plain config": {
			configuration: "[mysqld]\nmax_connections=1000\n",
			want:          "[mysqld]\nmax_connections=1000\n",
		},
		"template is returned unexpanded": {
			configuration: "[mysqld]\ninnodb_buffer_pool_size={{ containerMemoryLimit }}\n",
			want:          "[mysqld]\ninnodb_buffer_pool_size={{ containerMemoryLimit }}\n",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.MySQL.Configuration = tc.configuration
			c := Configurable(*cr)

			assert.Equal(t, tc.want, c.GetConfiguration())
		})
	}
}

func TestConfigurableGetResources(t *testing.T) {
	tests := map[string]struct {
		res corev1.ResourceRequirements
	}{
		"unset": {},
		"requests only": {
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
			},
		},
		"requests and limits": {
			res: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("2Gi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("2"),
					corev1.ResourceMemory: resource.MustParse("8Gi"),
				},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.MySQL.Resources = tc.res
			c := Configurable(*cr)

			assert.Equal(t, tc.res, c.GetResources())
		})
	}
}

func TestConfigurableExecuteConfigurationTemplate(t *testing.T) {
	tests := map[string]struct {
		input      string
		memory     *resource.Quantity
		want       string
		wantErrMsg string
	}{
		"plain text is returned unchanged": {
			input:  "[mysqld]\ninnodb_buffer_pool_size=128M",
			memory: resource.NewQuantity(1<<30, resource.BinarySI),
			want:   "[mysqld]\ninnodb_buffer_pool_size=128M",
		},
		"container memory limit is interpolated": {
			input:  "innodb_buffer_pool_size={{ containerMemoryLimit }}",
			memory: resource.NewQuantity(1<<30, resource.BinarySI),
			want:   "innodb_buffer_pool_size=1073741824",
		},
		"expression over the memory limit": {
			input:  "innodb_buffer_pool_size={{ containerMemoryLimit * 3 / 4 }}",
			memory: resource.NewQuantity(1<<30, resource.BinarySI),
			want:   "innodb_buffer_pool_size=805306368",
		},
		"unknown variable renders empty": {
			input:  "max_connections={{ nope }}",
			memory: resource.NewQuantity(1<<30, resource.BinarySI),
			want:   "max_connections=",
		},
		"file-access tags are banned": {
			input:      `{% include "/etc/passwd" %}`,
			memory:     resource.NewQuantity(1<<30, resource.BinarySI),
			wantErrMsg: "parse template",
		},
		"malformed template": {
			input:      "{% if %}",
			memory:     resource.NewQuantity(1<<30, resource.BinarySI),
			wantErrMsg: "parse template",
		},
		"filter failing at render time": {
			input:      `{{ containerMemoryLimit|date:"Y" }}`,
			memory:     resource.NewQuantity(1<<30, resource.BinarySI),
			wantErrMsg: "execute template",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			c := Configurable(*cr)

			got, err := c.ExecuteConfigurationTemplate(tc.input, tc.memory)

			if tc.wantErrMsg != "" {
				require.ErrorContains(t, err, tc.wantErrMsg)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestGetAutoTuneParams(t *testing.T) {
	tests := map[string]struct {
		configuration string
		memory        string
		want          string
		wantErrMsg    string
	}{
		"pool size under 1Gi keeps the default chunk size": {
			memory: "1Gi",
			want:   "\ninnodb_buffer_pool_size=536870912\ninnodb_buffer_pool_chunk_size=134217728\nmax_connections=85",
		},
		// A pool over 1Gi is split across 8 instances, so the chunk size grows with it.
		"pool size over 1Gi is spread over 8 instances": {
			memory: "8Gi",
			want:   "\ninnodb_buffer_pool_size=4294967296\ninnodb_buffer_pool_chunk_size=536870912\nmax_connections=682",
		},
		// 500Mi is not a multiple of the 128Mi chunk, so the pool is rounded up to 512Mi.
		"pool size is rounded up to a chunk multiple": {
			memory: "1000Mi",
			want:   "\ninnodb_buffer_pool_size=536870912\ninnodb_buffer_pool_chunk_size=134217728\nmax_connections=83",
		},
		"user-set buffer pool size skips both pool params": {
			configuration: "[mysqld]\ninnodb_buffer_pool_size=1G\n",
			memory:        "1Gi",
			want:          "\nmax_connections=85",
		},
		"user-set chunk size keeps the calculated pool size": {
			configuration: "[mysqld]\ninnodb_buffer_pool_chunk_size=64M\n",
			memory:        "1Gi",
			want:          "\ninnodb_buffer_pool_size=536870912\nmax_connections=85",
		},
		"user-set max connections is not overridden": {
			configuration: "[mysqld]\nmax_connections=1000\n",
			memory:        "1Gi",
			want:          "\ninnodb_buffer_pool_size=536870912\ninnodb_buffer_pool_chunk_size=134217728",
		},
		"lowest memory that still yields a connection": {
			memory: "12Mi",
			want:   "\ninnodb_buffer_pool_size=134217728\ninnodb_buffer_pool_chunk_size=134217728\nmax_connections=1",
		},
		"memory too small for max connections": {
			memory:     "10Mi",
			wantErrMsg: "not enough memory set in requests. Must be >= 12Mi",
		},
		// Without max_connections to calculate, the memory floor doesn't apply.
		"memory too small but max connections is user-set": {
			configuration: "[mysqld]\nmax_connections=1000\n",
			memory:        "10Mi",
			want:          "\ninnodb_buffer_pool_size=134217728\ninnodb_buffer_pool_chunk_size=134217728",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1.PerconaServerMySQL{}
			cr.Spec.MySQL.Configuration = tc.configuration
			q := resource.MustParse(tc.memory)

			got, err := GetAutoTuneParams(cr, &q)

			if tc.wantErrMsg != "" {
				require.EqualError(t, err, tc.wantErrMsg)
				assert.Empty(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
