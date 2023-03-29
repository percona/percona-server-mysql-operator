#!/bin/bash

set -o errexit
set -o xtrace

MYSQL_PORT=3306
MYSQLX_PORT=33060
MYSQL_ADMIN_PORT=33062

function main() {
	echo "Running $0"

	NODE_LIST=()
	NODE_LIST_REPL=()
	NODE_LIST_MYSQLX=()
	NODE_LIST_ADMIN=()

	SERVER_OPTIONS=${HA_SERVER_OPTIONS:-'check inter 10000 rise 1 fall 2 weight 1'}
	send_proxy=''
	path_to_haproxy_cfg='/etc/haproxy/mysql'
	if [[ ${IS_PROXY_PROTOCOL} == "yes" ]]; then
		send_proxy='send-proxy-v2'
	fi

	while read mysql_host; do
		if [ -z "$mysql_host" ]; then
			echo "Could not find PEERS ..."
			exit 0
		fi

		node_name=$(echo "$mysql_host" | cut -d . -f -1)
		NODE_LIST_REPL+=("server ${node_name} ${mysql_host}:${MYSQL_PORT} ${send_proxy} ${SERVER_OPTIONS}")
		NODE_LIST+=("server ${node_name} ${mysql_host}:${MYSQL_PORT} ${send_proxy} ${SERVER_OPTIONS}")
		NODE_LIST_ADMIN+=("server ${node_name} ${mysql_host}:${MYSQL_ADMIN_PORT} ${SERVER_OPTIONS}")
		NODE_LIST_MYSQLX+=("server ${node_name} ${mysql_host}:${MYSQLX_PORT} ${send_proxy} ${SERVER_OPTIONS}")
	done

	echo "${#NODE_LIST_REPL[@]}" >$path_to_haproxy_cfg/AVAILABLE_NODES

	cat <<-EOF >"$path_to_haproxy_cfg/haproxy.cfg"
		    backend mysql-primary
		      mode tcp
		      option srvtcpka
		      balance roundrobin
		      option external-check
		     external-check command /opt/percona/haproxy-check
	EOF

	(
		IFS=$'\n'
		echo "${NODE_LIST[*]}"
	) >>"$path_to_haproxy_cfg/haproxy.cfg"

	cat <<-EOF >>"$path_to_haproxy_cfg/haproxy.cfg"
		    backend mysql-replicas
		      mode tcp
		      option srvtcpka
		      balance roundrobin
		      option external-check
		      external-check command /opt/percona/haproxy-check
	EOF

	(
		IFS=$'\n'
		echo "${NODE_LIST_REPL[*]}"
	) >>"$path_to_haproxy_cfg/haproxy.cfg"

	haproxy -c -f /opt/percona/haproxy-global.cfg -f $path_to_haproxy_cfg/haproxy.cfg

	# TODO: Can we do something for maintenance mode?

	if [ -S "$path_to_haproxy_cfg/haproxy-main.sock" ]; then
		echo 'reload' | socat stdio "$path_to_haproxy_cfg/haproxy-main.sock"
	fi
}

main
exit 0
