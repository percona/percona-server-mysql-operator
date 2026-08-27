package mysql

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/flosch/pongo2"
	"github.com/go-ini/ini"
	v "github.com/hashicorp/go-version"
	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql/autoconfig"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

type Configurable apiv1.PerconaServerMySQL

func (c *Configurable) GetConfigMapName() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return ConfigMapName(&cr)
}

func (c *Configurable) GetConfigMapKey() string {
	return CustomConfigKey
}

func (c *Configurable) GetConfiguration() string {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Configuration
}

func (c *Configurable) GetResources() corev1.ResourceRequirements {
	cr := apiv1.PerconaServerMySQL(*c)
	return cr.Spec.MySQL.Resources
}

func (c *Configurable) ExecuteConfigurationTemplate(input string, memory *resource.Quantity) (string, error) {
	set, err := util.SandboxedTemplateSet()
	if err != nil {
		return "", errors.Wrap(err, "create sandboxed template set")
	}
	tmpl, err := set.FromString(input)
	if err != nil {
		return "", errors.Wrap(err, "parse template")
	}
	result, err := tmpl.Execute(pongo2.Context{"containerMemoryLimit": memory.Value()})
	if err != nil {
		return "", errors.Wrap(err, "execute template")
	}
	return result, nil
}

// loosePrefix matches the prefix that tells mysqld to ignore an option it
// doesn't recognize. It is not part of the variable's identity: both mysqld and
// SET GLOBAL read loose_x and x as the same variable, so the two spellings must
// never be treated as separate options.
var loosePrefix = regexp.MustCompile(`^loose[-_]`)

// IsLooseVariable reports whether key carries the loose prefix, which tells
// mysqld to ignore the option when the server doesn't know it.
func IsLooseVariable(key string) bool {
	return loosePrefix.MatchString(key)
}

// CanonicalVariableName strips the loose prefix so that aliases of the same
// mysqld variable compare equal.
func CanonicalVariableName(key string) string {
	return loosePrefix.ReplaceAllString(key, "")
}

// GetAutoTuneParams derives innodb_buffer_pool_size, innodb_buffer_pool_chunk_size
// and max_connections from the given memory quantity. Values the user already set
// in .spec.mysql.configuration are left untouched.
func GetAutoTuneParams(cr *apiv1.PerconaServerMySQL, q *resource.Quantity) (string, error) {
	autotuneParams := ""

	poolSize := q.Value() * int64(50) / int64(100)
	instances := int64(1)                 // default value
	chunkSize := int64(1024 * 1024 * 128) // default value

	// Adjust innodb_buffer_pool_chunk_size
	// If innodb_buffer_pool_size is bigger than 1Gi, innodb_buffer_pool_instances is set to 8.
	// By default, innodb_buffer_pool_chunk_size is 128Mi and innodb_buffer_pool_size needs to be
	// multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
	// More info: https://dev.mysql.com/doc/refman/8.0/en/innodb-buffer-pool-resize.html
	if poolSize > int64(1073741824) {
		instances = 8
		chunkSize = poolSize / instances
		// innodb_buffer_pool_chunk_size can be increased or decreased in units of 1Mi (1048576 bytes).
		// That's why we should strip redundant bytes
		chunkSize -= chunkSize % (1048576)
		poolSize = instances * chunkSize
	} else if poolSize%(instances*chunkSize) != 0 {
		// Buffer pool size must always
		// be equal to or a multiple of innodb_buffer_pool_chunk_size * innodb_buffer_pool_instances.
		// If not, this value will be adjusted
		poolSize += (instances * chunkSize) - poolSize%(instances*chunkSize)
	}

	conf := cr.Spec.MySQL.Configuration
	if !strings.Contains(conf, "innodb_buffer_pool_size") {
		poolSizeVal := strconv.FormatInt(poolSize, 10)
		autotuneParams += "\ninnodb_buffer_pool_size=" + poolSizeVal

		if !strings.Contains(conf, "innodb_buffer_pool_chunk_size") {
			chunkSizeVal := strconv.FormatInt(chunkSize, 10)
			autotuneParams += "\ninnodb_buffer_pool_chunk_size=" + chunkSizeVal
		}
	}

	if !strings.Contains(conf, "max_connections") {
		divider := int64(12582880)
		if q.Value() < divider {
			return "", errors.New("not enough memory set in requests. Must be >= 12Mi")
		}
		maxConnSize := q.Value() / divider
		maxConnSizeVal := strconv.FormatInt(maxConnSize, 10)
		autotuneParams += "\nmax_connections=" + maxConnSizeVal
	}

	return autotuneParams, nil
}

// EffectiveResource returns the quantity autoconfig should size the MySQL
// configuration against for the given resource, or nil when neither a limit nor
// a request is set.
func EffectiveResource(res corev1.ResourceRequirements, name corev1.ResourceName) *resource.Quantity {
	if q, ok := res.Limits[name]; ok {
		return &q
	}
	if q, ok := res.Requests[name]; ok {
		return &q
	}
	return nil
}

// GetAutoConfigParams derives a full, production-grade set of mysqld parameters
// from the pod's CPU/memory allocation, the configured workload profile, the
// given MySQL version and the replication topology, using the mysqloperatorcalculator library.
func GetAutoConfigParams(cr *apiv1.PerconaServerMySQL, version string, cpu, memory *resource.Quantity) (string, error) {
	if cpu == nil || cpu.IsZero() {
		return "", errors.New("cpu is required for autoconfig")
	}
	if memory == nil || memory.IsZero() {
		return "", errors.New("memory is required for autoconfig")
	}

	ver, err := parseMySQLVersion(version)
	if err != nil {
		return "", errors.Wrap(err, "parse mysql version")
	}

	dbType := autoconfig.DBTypeGroupReplication
	if cr.Spec.MySQL.IsAsync() {
		dbType = autoconfig.DBTypeAsync
	}

	res, err := autoconfig.Calculate(autoconfig.Request{
		DBType:      dbType,
		CPU:         int(cpu.MilliValue()),
		MemoryBytes: memory.Value(),
		Version:     ver,
		LoadType:    autoConfigLoadType(cr.Spec.MySQL.AutoConfig.LoadType),
	})
	if err != nil {
		return "", errors.Wrap(err, "calculate configuration")
	}

	params, err := res.MySQLdParams()
	if err != nil {
		return "", errors.Wrap(err, "get mysqld params")
	}

	userKeys, err := userConfigKeys(cr.Spec.MySQL.Configuration)
	if err != nil {
		return "", errors.Wrap(err, "parse user configuration")
	}

	// Sort for a stable ConfigMap payload so unchanged resources don't produce
	// a churning config hash and needless rollout restarts. Keys are compared
	// canonically, so a user's group_replication_x suppresses the calculator's
	// loose_group_replication_x rather than leaving both spellings of the same
	// variable in the merged configuration.
	names := make([]string, 0, len(params))
	for name := range params {
		if _, ok := userKeys[CanonicalVariableName(name)]; ok {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString("\n")
		b.WriteString(name)
		b.WriteString("=")
		b.WriteString(params[name])
	}
	return b.String(), nil
}

func autoConfigLoadType(lt apiv1.AutoConfigLoadType) int {
	switch lt {
	case apiv1.AutoConfigLoadTypeMostlyReads:
		return autoconfig.LoadTypeMostlyReads
	case apiv1.AutoConfigLoadTypeEqualReadsWrites:
		return autoconfig.LoadTypeEqualReadsWrites
	case apiv1.AutoConfigLoadTypeHeavyWrites:
		return autoconfig.LoadTypeHeavyWrites
	default:
		return autoconfig.LoadTypeSomeWrites
	}
}

func parseMySQLVersion(s string) (autoconfig.Version, error) {
	if strings.TrimSpace(s) == "" {
		return autoconfig.Version{}, errors.New("version is empty")
	}
	parsed, err := v.NewVersion(s)
	if err != nil {
		return autoconfig.Version{}, err
	}
	seg := parsed.Segments()
	ver := autoconfig.Version{}
	if len(seg) > 0 {
		ver.Major = seg[0]
	}
	if len(seg) > 1 {
		ver.Minor = seg[1]
	}
	if len(seg) > 2 {
		ver.Patch = seg[2]
	}
	return ver, nil
}

// userConfigKeys returns the canonical names of the variables the user set in
// .spec.mysql.configuration.
func userConfigKeys(configuration string) (map[string]struct{}, error) {
	keys := make(map[string]struct{})
	if strings.TrimSpace(configuration) == "" {
		return keys, nil
	}
	section, err := config.ParseSection(io.NopCloser(strings.NewReader(configuration)), "mysqld")
	if err != nil {
		return nil, err
	}
	for _, k := range section.Keys() {
		keys[CanonicalVariableName(k.Name())] = struct{}{}
	}
	return keys, nil
}

func GetConfig(
	ctx context.Context,
	cl client.Reader,
	cr *apiv1.PerconaServerMySQL,
) (config.Section, error) {
	configurable := Configurable(*cr)
	cmName := configurable.GetConfigMapName()
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}
	parts := make([]string, 0, 3)

	autoCM := &corev1.ConfigMap{}
	autoNN := types.NamespacedName{Name: AutoConfigMapName(cr), Namespace: cr.Namespace}
	if err := cl.Get(ctx, autoNN, autoCM); client.IgnoreNotFound(err) != nil {
		return config.EmptySection, errors.Wrap(err, "get auto configmap")
	} else if err == nil {
		parts = append(parts, readConfig(autoCM, configurable))
	}

	cm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, nn, cm); client.IgnoreNotFound(err) != nil {
		return config.EmptySection, errors.Wrap(err, "get configmap")
	} else if err == nil {
		parts = append(parts, readConfig(cm, configurable))
	}

	secret := &corev1.Secret{}
	if err := cl.Get(ctx, nn, secret); client.IgnoreNotFound(err) != nil {
		return config.EmptySection, errors.Wrap(err, "get secret")
	} else if err == nil {
		parts = append(parts, readConfig(secret, configurable))
	}

	if len(parts) == 0 {
		return config.EmptySection, nil
	}

	for i, part := range parts {
		parts[i] = withMySQLdSection(part)
	}

	merged := strings.Join(parts, "\n")
	section, err := config.ParseSection(io.NopCloser(strings.NewReader(merged)), "mysqld")
	if err != nil {
		return config.EmptySection, errors.Wrap(err, "parse config section")
	}

	dropAliasedKeys(section)

	result := config.Section{Section: *section}
	return result, nil
}

// dropAliasedKeys keeps a single key per canonical variable name, the last one,
// so that the same variable spelled two ways collapses the way the ini parser
// already collapses identical spellings. Parts are merged auto-config first and
// user configuration last, so the surviving key is the user's. Without this the
// section would carry both loose_x and x, and the dynamic-configuration
// reconciler would issue a SET GLOBAL for each in map iteration order, letting
// a different value win on each pod.
func dropAliasedKeys(section *ini.Section) {
	lastByCanonical := make(map[string]string, len(section.Keys()))
	for _, k := range section.Keys() {
		lastByCanonical[CanonicalVariableName(k.Name())] = k.Name()
	}

	for _, k := range section.Keys() {
		if lastByCanonical[CanonicalVariableName(k.Name())] != k.Name() {
			section.DeleteKey(k.Name())
		}
	}
}

func withMySQLdSection(part string) string {
	for _, line := range strings.Split(part, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			return part
		}
		break
	}
	return "[mysqld]\n" + part
}

func readConfig(object client.Object, cfg Configurable) string {
	switch obj := object.(type) {
	case *corev1.ConfigMap:
		return obj.Data[cfg.GetConfigMapKey()]
	case *corev1.Secret:
		return string(obj.Data[cfg.GetConfigMapKey()])
	default:
		return ""
	}
}

func GetLastAppliedConfig(
	sts *appsv1.StatefulSet,
) (config.Section, error) {
	val, ok := sts.GetAnnotations()[naming.AnnotationLastAppliedConfig.String()]
	if !ok {
		return config.EmptySection, nil
	}

	result, err := config.NewSection(config.NewSectionOpts{})
	if err != nil {
		return config.EmptySection, errors.Wrap(err, "create section")
	}

	if err := result.FromJSON(io.NopCloser(strings.NewReader(val)), "mysqld"); err != nil {
		return config.EmptySection, errors.Wrap(err, "parse section from JSON")
	}
	return *result, nil
}

func FormatConfigValue(value string) string {
	value = strings.TrimSpace(value)

	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return value
	}

	if expanded, ok := expandByteSuffix(value); ok {
		return expanded
	}

	switch strings.ToLower(value) {
	case "true", "on":
		return "ON"
	case "false", "off":
		return "OFF"
	}

	return QuoteLiteral(value)
}

// QuoteLiteral renders value as a single SQL string literal, escaped so no part
// of it can be parsed as SQL. Quotes are doubled rather than backslash-escaped
// because doubling is the only form that holds under NO_BACKSLASH_ESCAPES too,
// and sql_mode is itself one of the variables we set.
func QuoteLiteral(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `''`)
	return fmt.Sprintf("'%s'", escaped)
}

func expandByteSuffix(value string) (string, bool) {
	// Quantity parses a bare "Gi" as zero, so require a leading digit rather
	// than turning a mangled value into a silent 0.
	if value == "" || value[0] < '0' || value[0] > '9' {
		return "", false
	}

	q, err := resource.ParseQuantity(strings.ToUpper(value) + "i")
	if err != nil {
		return "", false
	}

	return strconv.FormatInt(q.Value(), 10), true
}
