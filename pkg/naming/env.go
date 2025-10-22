package naming

const (
	EnvMySQLStateFile    = "MYSQL_STATE_FILE"
	EnvMySQLNotifySocket = "MYSQL_NOTIFY_SOCKET"

	EnvMySQLNotifySocketInternal = "NOTIFY_SOCKET" // should only be set in the entrypoint

	EnvMySQLClusterType = "CLUSTER_TYPE"

	EnvBootstrapReadTimeout = "BOOTSTRAP_READ_TIMEOUT"

	EnvBootstrapCloneTimeout = "BOOTSTRAP_CLONE_TIMEOUT"
)
