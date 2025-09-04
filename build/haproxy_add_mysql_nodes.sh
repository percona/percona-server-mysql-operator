#!/bin/bash

set -o errexit

log() {
	local message=$1
	local date=$(/usr/bin/date +"%d/%b/%Y:%H:%M:%S.%3N")

	echo "{\"time\":\"${date}\", \"message\": \"${message}\"}"
}

MYSQL_PORT=3306
MYSQLX_PORT=33060
MYSQL_ADMIN_PORT=33062
primary_mysql_node=''
primary_mysql_host=''

function main() {
	log "Running $0"

	NODE_LIST=()
	NODE_LIST_REPL=()
	NODE_LIST_MYSQLX=()
	NODE_LIST_ADMIN=()

	SERVER_OPTIONS=${HA_SERVER_OPTIONS:-'resolvers kubernetes inter 10000 rise 1 fall 2 check weight 1'}
	send_proxy=''
	path_to_haproxy_cfg='/etc/haproxy/mysql'
	if [[ ${IS_PROXY_PROTOCOL} == "yes" ]]; then
		send_proxy='send-proxy-v2'
	fi

	while read mysql_host; do
		if [ -z "$mysql_host" ]; then
			log 'Could not find PEERS ...'
			exit 0
		fi

		node_name=$(echo "$mysql_host" | cut -d . -f -1)
		if /opt/percona/haproxy_check_primary.sh '' '' "$mysql_host";then
			primary_mysql_host="$mysql_host"
			primary_mysql_node="server ${node_name} ${mysql_host}:${MYSQL_PORT} ${send_proxy} ${SERVER_OPTIONS} on-marked-up shutdown-backup-sessions"
			continue
		fi

		NODE_LIST_REPL+=("server ${node_name} ${mysql_host}:${MYSQL_PORT} ${send_proxy} ${SERVER_OPTIONS}")
		NODE_LIST+=("server ${node_name} ${mysql_host}:${MYSQL_PORT} ${send_proxy} ${SERVER_OPTIONS} backup")
		NODE_LIST_ADMIN+=("server ${node_name} ${mysql_host}:${MYSQL_ADMIN_PORT} ${SERVER_OPTIONS} backup")
		NODE_LIST_MYSQLX+=("server ${node_name} ${mysql_host}:${MYSQLX_PORT} ${send_proxy} ${SERVER_OPTIONS} backup")
	done

	if [ -n "$primary_mysql_host" ]; then
		if [[ "${#NODE_LIST[@]}" -ne 0 ]]; then
			NODE_LIST=("$primary_mysql_node" "$(printf '%s\n' "${NODE_LIST[@]}" | sort --version-sort -r | uniq)")
			NODE_LIST_ADMIN=("$primary_mysql_node" "$(printf '%s\n' "${NODE_LIST_ADMIN[@]}" | sort --version-sort -r | uniq)")
			NODE_LIST_MYSQLX=("$primary_mysql_node" "$(printf '%s\n' "${NODE_LIST_MYSQLX[@]}" | sort --version-sort -r | uniq)")
		else
			NODE_LIST=("$primary_mysql_node")
			NODE_LIST_ADMIN=("$primary_mysql_node")
			NODE_LIST_MYSQLX=("$primary_mysql_node")
		fi
	else
		if [[ "${#NODE_LIST[@]}" -ne 0 ]]; then
			NODE_LIST=("$(printf '%s\n' "${NODE_LIST[@]}" | sort --version-sort -r | uniq)")
			NODE_LIST_ADMIN=("$(printf '%s\n' "${NODE_LIST_ADMIN[@]}" | sort --version-sort -r | uniq)")
			NODE_LIST_MYSQLX=("$(printf '%s\n' "${NODE_LIST_MYSQLX[@]}" | sort --version-sort -r | uniq)")
		fi
	fi


	echo "${#NODE_LIST_REPL[@]}" >$path_to_haproxy_cfg/AVAILABLE_NODES
	log "number of available nodes are ${#NODE_LIST_REPL[@]}"

	haproxy_check_script='haproxy_check_primary.sh'
	if [ "${#NODE_LIST_REPL[@]}" -gt 1 ]; then
		haproxy_check_script='haproxy_check_replicas.sh'
	fi

	cat <<-EOF >"$path_to_haproxy_cfg/haproxy.cfg"
		    backend mysql-primary
		      mode tcp
		      option srvtcpka
		      balance roundrobin
		      option external-check
		      external-check command /opt/percona/haproxy_check_primary.sh
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
		      external-check command /opt/percona/haproxy_check_replicas.sh
	EOF

	(
		IFS=$'\n'
		echo "${NODE_LIST_REPL[*]}"
	) >>"$path_to_haproxy_cfg/haproxy.cfg"

	cat <<-EOF >>"$path_to_haproxy_cfg/haproxy.cfg"
		    backend mysql-x
		      mode tcp
		      option srvtcpka
		      balance roundrobin
		      option external-check
		      external-check command /opt/percona/$haproxy_check_script
	EOF

	(
		IFS=$'\n'
		echo "${NODE_LIST_MYSQLX[*]}"
	) >>"$path_to_haproxy_cfg/haproxy.cfg"

	cat <<-EOF >>"$path_to_haproxy_cfg/haproxy.cfg"
		    backend mysql-admin
		      mode tcp
		      option srvtcpka
		      balance roundrobin
		      option external-check
		      external-check command /opt/percona/$haproxy_check_script
	EOF
	(
		IFS=$'\n'
		echo "${NODE_LIST_ADMIN[*]}"
	) >>"$path_to_haproxy_cfg/haproxy.cfg"

	path_to_custom_global_cnf='/etc/haproxy-custom'
	if [ -f "$path_to_custom_global_cnf/haproxy-global.cfg" ]; then
		haproxy -c -f "$path_to_custom_global_cnf/haproxy-global.cfg" -f $path_to_haproxy_cfg/haproxy.cfg
	fi

	haproxy -c -f /opt/percona/haproxy-global.cfg -f $path_to_haproxy_cfg/haproxy.cfg

	# TODO: Can we do something for maintenance mode?
	if [ -S "$path_to_haproxy_cfg/haproxy-main.sock" ]; then
		log "reload | socat stdio $path_to_haproxy_cfg/haproxy-main.sock"
		echo 'reload' | socat stdio "$path_to_haproxy_cfg/haproxy-main.sock"
	fi
}

main
exit 0
