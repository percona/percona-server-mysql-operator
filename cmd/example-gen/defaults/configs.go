package defaults

const (
	ConfigurationHAProxy = `#      the actual default configuration file can be found here https://github.com/percona/percona-server-mysql-operator/blob/main/build/haproxy-global.cfg

global
  maxconn 2048
  external-check
  insecure-fork-wanted
  stats socket /etc/haproxy/mysql/haproxy.sock mode 600 expose-fd listeners level admin

defaults
  default-server init-addr last,libc,none
  log global
  mode tcp
  retries 10
  timeout client 28800s
  timeout connect 100500
  timeout server 28800s

frontend mysql-primary-in
  bind *:3309 accept-proxy
  bind *:3306
  mode tcp
  option clitcpka
  default_backend mysql-primary

frontend mysql-replicas-in
  bind *:3307
  mode tcp
  option clitcpka
  default_backend mysql-replicas

frontend stats
  bind *:8404
  mode http
  http-request use-service prometheus-exporter if { path /metrics }`

	ConfigurationMySQL = `max_connections=250
innodb_buffer_pool_size={{containerMemoryLimit * 3/4}}`

	ConfigurationRouter = `[default]
logging_folder=/tmp/router/log
[logger]
level=DEBUG`
)
