package naming

type MySQLState string

const (
	MySQLReady   MySQLState = "ready"
	MySQLDown    MySQLState = "down"
	MySQLStartup MySQLState = "startup"
	MySQLUnknown MySQLState = "unknown"
)
