#!/bin/bash

set -e
set -o xtrace

MYSQL_PORT=3306
MYSQLX_PORT=33060
MYSQL_ADMIN_PORT=33062

# must be >= max cluster size
MAX_SLOTS="${HA_MAX_SLOTS:-9}"

path_to_haproxy_cfg='/etc/haproxy/mysql'

SERVER_OPTIONS=${HA_SERVER_OPTIONS:-'resolvers kubernetes inter 10000 rise 1 fall 2 check weight 1'}

send_proxy=''
if [[ ${IS_PROXY_PROTOCOL} == "yes" ]]; then
	send_proxy='send-proxy-v2'
fi

log() {
	local message=$1
	local date=$(/usr/bin/date +"%d/%b/%Y:%H:%M:%S.%3N")

	echo "{\"time\":\"${date}\", \"message\": \"${message}\"}"
}

emit_backend() {
	local name=$1
	local port=$2
	local check_script=$3
	local extra=$4

	cat <<-EOF
		backend ${name}
		  mode tcp
		  option srvtcpka
		  balance roundrobin
		  option external-check
		  external-check command /opt/percona/${check_script}
		  default-server ${SERVER_OPTIONS} init-addr none on-marked-down shutdown-sessions ${extra}
	EOF

	# pre-provisioned slots; disabled == start in MAINT, no checks until
	# peer-list script sets a real fqdn and flips them to ready
	local i
	for i in $(seq 0 $((MAX_SLOTS - 1))); do
		echo "  server ${CLUSTER_NAME}-mysql-${i} ${CLUSTER_NAME}-mysql-${i}.${CLUSTER_NAME}-mysql:${port} disabled"
	done
}

echo "${CLUSTER_TYPE}" >/tmp/cluster_type

if [ "$1" = 'haproxy' ]; then
  if [ ! -f '/etc/haproxy/mysql/haproxy.cfg' ]; then
    cp /opt/percona/haproxy.cfg /etc/haproxy/mysql
  fi

	{
		emit_backend mysql-primary "$MYSQL_PORT" haproxy_check_primary.sh "$send_proxy"
		emit_backend mysql-replicas "$MYSQL_PORT" haproxy_check_replicas.sh "$send_proxy"
		emit_backend mysql-x "$MYSQLX_PORT" haproxy_check_replicas.sh "$send_proxy"
		emit_backend mysql-admin "$MYSQL_ADMIN_PORT" haproxy_check_replicas.sh ''
	} >"$path_to_haproxy_cfg/haproxy.cfg"

	path_to_custom_global_cnf='/etc/haproxy-custom'
	if [ -f "$path_to_custom_global_cnf/haproxy-global.cfg" ]; then
		haproxy -c -f "$path_to_custom_global_cnf/haproxy-global.cfg" -f "$path_to_haproxy_cfg/haproxy.cfg"
	fi

	haproxy -c -f /opt/percona/haproxy-global.cfg -f "$path_to_haproxy_cfg/haproxy.cfg"

  custom_conf='/etc/haproxy-custom/haproxy.cfg'
  if [ -f "$custom_conf" ]; then
    log "haproxy -c -f $custom_conf -f /etc/haproxy/mysql/haproxy.cfg"
    haproxy -c -f $custom_conf -f /etc/haproxy/mysql/haproxy.cfg || EC=$?
    if [ -n "$EC" ]; then
      log "The custom config $custom_conf is not valid and will be ignored."
    fi
  fi

  haproxy_opt='-W -db '
  if [ -f "$custom_conf" -a -z "$EC" ]; then
    haproxy_opt+="-f $custom_conf "
  else
    haproxy_opt+='-f /opt/percona/haproxy-global.cfg '
  fi

  haproxy_opt+='-f /etc/haproxy/mysql/haproxy.cfg -p /etc/haproxy/mysql/haproxy.pid -S /etc/haproxy/mysql/haproxy-main.sock'

  if [ -f '/etc/haproxy/config/haproxy.cfg' ]; then
    haproxy_opt="${haproxy_opt} -f /etc/haproxy/config/haproxy.cfg"
  fi
fi

DEFAULT_RLIMIT_NOFILE=1048576
RLIMIT_NOFILE=${HA_RLIMIT_NOFILE:-${DEFAULT_RLIMIT_NOFILE}}
hard_limit=$(ulimit -Hn)
if ! [[ ${RLIMIT_NOFILE} =~ ^[0-9]+$ ]]; then
	log "HA_RLIMIT_NOFILE is not a valid integer ('${RLIMIT_NOFILE}'), falling back to ${DEFAULT_RLIMIT_NOFILE}."
	RLIMIT_NOFILE=${DEFAULT_RLIMIT_NOFILE}
fi
if [[ ${hard_limit} =~ ^[0-9]+$ ]] && [[ ${RLIMIT_NOFILE} -gt ${hard_limit} ]]; then
	log "Requested open file limit (${RLIMIT_NOFILE}) exceeds hard limit (${hard_limit}), clamping."
	RLIMIT_NOFILE=${hard_limit}
fi
if ! ulimit -n "${RLIMIT_NOFILE}"; then
	log "Failed to set open file limit to ${RLIMIT_NOFILE}, continuing with $(ulimit -n)."
fi

exec "$@" ${haproxy_opt}
