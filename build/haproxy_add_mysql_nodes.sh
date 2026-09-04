#!/bin/bash

set -o errexit

log() {
	local message=$1
	local date=$(/usr/bin/date +"%d/%b/%Y:%H:%M:%S.%3N")

	echo "{\"time\":\"${date}\", \"message\": \"${message}\"}"
}

SOCK="${HA_STATS_SOCKET:-/etc/haproxy/mysql/haproxy.sock}"
MAX_SLOTS="${HA_MAX_SLOTS:-9}"
path_to_haproxy_cfg='/etc/haproxy/mysql'
BACKENDS='mysql-primary mysql-replicas mysql-x mysql-admin'

haproxy_socket_cmd() {
	local cmd=$1
	local out

	out=$(echo "$cmd" | socat -t 5 stdio "$SOCK" | tr -d '\n')
	if [ -n "$out" ]; then
		log "'${cmd}' -> ${out}"
	fi
	return 0
}

main() {
	log "Running $0"

	local peers=()
	local mysql_host
	while read -r mysql_host; do
		if [ -n "$mysql_host" ]; then
			peers+=("$mysql_host")
		fi
	done

	if [ "${#peers[@]}" -eq 0 ]; then
		log 'Could not find PEERS ...'
		exit 0
	fi

	# stable slot assignment: version-sort == pod ordinal order for statefulsets
	mapfile -t peers < <(printf '%s\n' "${peers[@]}" | sort --version-sort | uniq)

	if [ "${#peers[@]}" -gt "$MAX_SLOTS" ]; then
		log "ERROR: ${#peers[@]} peers but only ${MAX_SLOTS} slots; raise HA_MAX_SLOTS"
		exit 1
	fi

	# replica count for the check scripts: peers minus primary (if one exists)
	local n_replicas=${#peers[@]}
	for mysql_host in "${peers[@]}"; do
		if /opt/percona/haproxy_check_primary.sh '' '' "$mysql_host"; then
			n_replicas=$((n_replicas - 1))
			break
		fi
	done

	echo "$n_replicas" >"$path_to_haproxy_cfg/AVAILABLE_NODES"
	log "number of available nodes are ${n_replicas}"

	local be i
	for be in $BACKENDS; do
		i=0
		for mysql_host in "${peers[@]}"; do
			haproxy_socket_cmd "set server ${be}/${CLUSTER_NAME}-mysql-${i} fqdn ${mysql_host}"
			haproxy_socket_cmd "set server ${be}/${CLUSTER_NAME}-mysql-${i} state ready"
			i=$((i + 1))
		done

		# park unused slots and kill any sessions still pinned to them
		while [ "$i" -lt "$MAX_SLOTS" ]; do
			haproxy_socket_cmd "set server ${be}/${CLUSTER_NAME}-mysql-${i} state maint"
			haproxy_socket_cmd "shutdown sessions server ${be}/${CLUSTER_NAME}-mysql-${i}"
			i=$((i + 1))
		done
	done
}

main
exit 0
