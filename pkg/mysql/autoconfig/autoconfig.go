package autoconfig

import (
	"github.com/pkg/errors"

	mysqlcalc "github.com/Tusamarco/mysqloperatorcalculator/src/mysqloperatorcalculator"
)

// DB replication topologies accepted by Request.DBType.
const (
	DBTypeGroupReplication = mysqlcalc.DbTypeGroupReplication
	DBTypeAsync            = mysqlcalc.DbTypeAsync
	DBTypePXC              = mysqlcalc.DbTypePXC
)

// Workload read/write profiles accepted by Request.LoadType.
const (
	LoadTypeMostlyReads      = mysqlcalc.LoadTypeMostlyReads      // ~95% reads
	LoadTypeSomeWrites       = mysqlcalc.LoadTypeSomeWrites       // ~80% reads
	LoadTypeEqualReadsWrites = mysqlcalc.LoadTypeEqualReadsWrites // ~50/50
	LoadTypeHeavyWrites      = mysqlcalc.LoadTypeHeavyWrites      // write dominated
)

var (
	ErrMemoryRequired = errors.New("memory is required")
	ErrCPURequired    = errors.New("cpu is required")
)

// Version identifies the target MySQL server version.
type Version struct {
	Major int
	Minor int
	Patch int
}

// Request describes the workload the operator wants MySQL tuned for. Only CPU
// and Memory are strictly required; the rest fall back to sensible defaults.
type Request struct {
	// DBType is the replication topology (one of the DBType* constants).
	// Defaults to group replication.
	DBType string
	// CPU is the CPU allocation for the whole pod in millicores (e.g. 4000 = 4 cores).
	CPU int
	// Memory is the memory allocation for the whole pod as a human-readable
	// string, e.g. "2.5G". Ignored when MemoryBytes is set.
	Memory string
	// MemoryBytes is the memory allocation for the whole pod in bytes. When
	// greater than zero it takes precedence over Memory, letting callers that
	// already hold an exact byte count skip string parsing.
	MemoryBytes int64
	// Connections is the target number of client connections.
	Connections int
	// Version is the MySQL server version being configured.
	Version Version
	// LoadType selects the read/write profile (one of the LoadType* constants).
	// Defaults to LoadTypeSomeWrites.
	LoadType int
	// ProviderCostPct optionally reserves a fraction (0..1) of the allocated
	// resources to account for provider overhead. Zero disables the adjustment.
	ProviderCostPct float64
	// SharedResources declares that CPU and Memory are a budget mysqld shares
	// with the proxy and monitoring components, and asks the calculator to split
	// it between them. The operator runs the proxies in their own pods and gives
	// the monitoring sidecar its own allocation, so its request describes an
	// instance dedicated to mysqld - the zero value.
	SharedResources bool
}

// Result holds the outcome of a Calculate call and the accessors the operator
// uses to read the tuned configuration back out.
type Result struct {
	// Message is the calculator's status message (warnings, notes). A MType > 0
	// signals the caller may want to log it.
	Message mysqlcalc.ResponseMessage
	// Families is the raw calculator output, keyed by family (mysql, proxy,
	// monitor), for callers that need groups beyond mysqld.
	Families map[string]mysqlcalc.Family

	calc *mysqlcalc.MysqlOperatorCalculator
	req  mysqlcalc.ConfigurationRequest
}

// Calculate runs the operator calculator for the given request and returns the
// tuned configuration. It returns an error if the request is malformed or the
// calculator cannot produce a result.
func Calculate(req Request) (*Result, error) {
	if req.Memory == "" && req.MemoryBytes == 0 {
		return nil, ErrMemoryRequired
	}
	if req.CPU == 0 {
		return nil, ErrCPURequired
	}
	if req.DBType == "" {
		req.DBType = DBTypeGroupReplication
	}
	if req.LoadType == 0 {
		req.LoadType = LoadTypeSomeWrites
	}

	moReq := mysqlcalc.ConfigurationRequest{
		DBType:          req.DBType,
		LoadType:        mysqlcalc.LoadType{Id: req.LoadType},
		Connections:     req.Connections,
		Output:          mysqlcalc.ResultOutputFormatJson,
		Mysqlversion:    mysqlcalc.Version{Major: req.Version.Major, Minor: req.Version.Minor, Patch: req.Version.Patch},
		ProviderCostPct: req.ProviderCostPct,
		MySQLDedicated:  !req.SharedResources,
		Dimension: mysqlcalc.Dimension{
			Id:     mysqlcalc.DimensionOpen,
			Cpu:    req.CPU,
			Memory: req.Memory,
		},
	}

	if req.MemoryBytes > 0 {
		moReq.Dimension.MemoryBytes = float64(req.MemoryBytes)
	} else {
		memBytes, err := moReq.Dimension.ConvertMemoryToBytes(req.Memory)
		if err != nil {
			return nil, errors.Wrapf(err, "convert memory %q to bytes", req.Memory)
		}
		moReq.Dimension.MemoryBytes = memBytes
	}

	var conf mysqlcalc.Configuration
	conf.Init()

	moc := &mysqlcalc.MysqlOperatorCalculator{}
	effectiveReq := moc.Init(moReq, conf)

	calcErr, msg, families := moc.GetCalculate()
	if calcErr != nil {
		return nil, errors.Wrap(calcErr, "calculate mysql configuration")
	}

	return &Result{
		Message:  msg,
		Families: families,
		calc:     moc,
		req:      effectiveReq,
	}, nil
}

// MySQLdConfig returns the tuned mysqld parameters rendered as an INI section,
// suitable for writing into the auto-config ConfigMap.
func (r *Result) MySQLdConfig() (string, error) {
	family, err := r.calc.GetFamily(mysqlcalc.FamilyTypeMysql)
	if err != nil {
		return "", errors.Wrap(err, "get mysql family")
	}
	buf, err := family.ParseFamilyGroup(mysqlcalc.GroupNameMySQLd, "")
	if err != nil {
		return "", errors.Wrap(err, "parse mysqld group")
	}
	return buf.String(), nil
}

// MySQLdParams returns the tuned mysqld parameters as a name/value map for
// callers that assemble configuration programmatically rather than as INI text.
func (r *Result) MySQLdParams() (map[string]string, error) {
	family, err := r.calc.GetFamily(mysqlcalc.FamilyTypeMysql)
	if err != nil {
		return nil, errors.Wrap(err, "get mysql family")
	}

	params := make(map[string]string)
	for name, group := range family.Groups {
		if name == mysqlcalc.GroupNameProbes || name == mysqlcalc.GroupNameResources ||
			name == "readinessProbe" || name == "livenessProbe" {
			continue
		}
		for _, p := range group.Parameters {
			params[p.Name] = p.Value
		}
	}
	return params, nil
}
