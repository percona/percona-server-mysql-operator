#!/bin/bash

set -e

ORC_HOST=127.0.0.1:3000

ORC_CONF_PATH=${ORC_CONF_PATH:-/etc/orchestrator}
TOPOLOGY_USER=${ORC_TOPOLOGY_USER:-orchestrator}
CREDS_FILE="${ORC_CONF_PATH}/orchestrator-users-secret/${TOPOLOGY_USER}"

TOPOLOGY_PASSWORD=""
if [ -f "${CREDS_FILE}" ]; then
	TOPOLOGY_PASSWORD=$(<"${CREDS_FILE}")
fi
TOPOLOGY_PASSWORD="${TOPOLOGY_PASSWORD:-$ORC_TOPOLOGY_PASSWORD}"

CURL_AUTH=()
if [ "${ORC_API_AUTH}" == "true" ] && [ -n "${TOPOLOGY_PASSWORD}" ]; then
	CURL_AUTH=(-u "${TOPOLOGY_USER}:${TOPOLOGY_PASSWORD}")
fi

log() {
	local level=$1
	local message=$2
	local now=$(date +%Y-%m-%dT%H:%M:%S%z)

	echo "${now} [${level}] ${message}"
}

wait_for_leader() {
	local retry=0
	local leader=""

	log WARNING "Waiting for leader. Will fail after 60 attempts"

	until [[ ${leader} != "" ]]; do
		if [ ${retry} -gt 60 ]; then
			exit 1
		fi

		leader=$(curl -fs "${CURL_AUTH[@]}" "${ORC_HOST}/api/raft-leader" 2>/dev/null) || true

		retry=$((retry + 1))
		sleep 1
	done
}

am_i_leader() {
	local http_code=$(curl "${CURL_AUTH[@]}" -o /dev/null -s -w '%{http_code}' "${ORC_HOST}/api/leader-check" 2>/dev/null)

	if [ "${http_code}" != "200" ]; then
		return 1
	fi

	log INFO "I am the leader"
	return 0
}

discover() {
	local host=$1
	local port=$2

	HOSTNAME=$(curl -s "${CURL_AUTH[@]}" "${ORC_HOST}/api/instance/${host}/${port}" | jq -r '.InstanceAlias // empty' 2>/dev/null) || true
	if [ -n "$HOSTNAME" ]; then
		log INFO "The MySQL node ${host} is already discovered by orchestrator. Skipping..."
		return 0
	fi

	for i in {1..5}; do
		R_CODE=$(curl -s "${CURL_AUTH[@]}" "${ORC_HOST}/api/discover/${host}/${port}" | jq -r '.Code // empty' 2>/dev/null) || true
		if [ "$R_CODE" == 'OK' ]; then
			log INFO "MySQL node ${host} is discovered"
			break
		fi
		log ERROR "MySQL node ${host} can't be discovered"
		sleep 1
	done
}

main() {
	log INFO "Starting to discover MySQL nodes."

	# Wait for the leader election
	wait_for_leader

	log INFO "Orchestrator cluster selected a leader."

	local retry=0
	# Exit if not leader
	while ! am_i_leader; do
		if [ ${retry} -gt 30 ]; then
			log INFO "I am not the leader. Exiting..."
			exit 0
		fi

		retry=$((retry + 1))
		sleep 1
	done

	# Discover
	while read mysql_host; do
		if [ -z "$mysql_host" ]; then
			log INFO "Could not find PEERS..."
			exit 0
		fi

		discover "${mysql_host}" 3306
	done
}

main
