package naming

const (
	EnvMySQLStateFile    = "MYSQL_STATE_FILE"
	EnvMySQLNotifySocket = "MYSQL_NOTIFY_SOCKET"

	EnvMySQLNotifySocketInternal = "NOTIFY_SOCKET" // should only be set in the entrypoint

	EnvBootstrapReadTimeout  = "BOOTSTRAP_READ_TIMEOUT"
	EnvMysqlshUserConfigHome = "MYSQLSH_USER_CONFIG_HOME"
)
