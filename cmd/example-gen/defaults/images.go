package defaults

const (
	ImageInitContainer = "perconalab/percona-server-mysql-operator:main"
	ImageMySQL         = "perconalab/percona-server-mysql-operator:main-psmysql8.4"
	ImageHAProxy       = "perconalab/percona-server-mysql-operator:main-haproxy"
	ImageRouter        = "perconalab/percona-server-mysql-operator:main-router8.4"
	ImageOrchestrator  = "perconalab/percona-server-mysql-operator:main-orchestrator"
	ImagePMM           = "perconalab/pmm-client:3-dev-latest"
	ImageBackup        = "perconalab/percona-server-mysql-operator:main-backup8.4"
	ImageToolkit       = "perconalab/percona-server-mysql-operator:main-toolkit"
)
