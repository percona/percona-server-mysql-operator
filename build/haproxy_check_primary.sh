#!/bin/bash

set -e

log() {
	local level=$1
	local message=$2
	local date=$(/usr/bin/date +"%d/%b/%Y:%H:%M:%S.%3N")

	echo "{\"time\":\"${date}\", \"level\": \"${level}\", \"message\": \"${message}\"}"
}

MYSQL_SERVER_IP=$3
MYSQL_SERVER_PORT='33062'

MONITOR_USER='monitor'
MONITOR_PASSWORD=$(/bin/cat /etc/mysql/mysql-users-secret/monitor)

TIMEOUT=${HA_CONNECTION_TIMEOUT:-10}
MYSQL_CMDLINE="/usr/bin/timeout $TIMEOUT /usr/bin/mysql -nNE -u${MONITOR_USER} -h ${MYSQL_SERVER_IP} -P ${MYSQL_SERVER_PORT}"

CLUSTER_TYPE=$(/bin/cat /tmp/cluster_type)

check_async() {
	READ_ONLY=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e 'select @@super_read_only' | /usr/bin/sed -n -e '2p' | /usr/bin/tr -d '\n')

	# ${REPLICATION_STATUS[0]} - Replica_IO_Running
	# ${REPLICATION_STATUS[1]} - Replica_SQL_Running
	REPLICATION_STATUS=($(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e 'SHOW REPLICA STATUS' | /usr/bin/sed -n -e '12p' -e '13p' | /usr/bin/tr '\n' ' '))

	log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} super_read_only: ${READ_ONLY} Replica_IO_Running: ${REPLICATION_STATUS[0]} Replica_SQL_Running: ${REPLICATION_STATUS[1]}"

	if [[ ${READ_ONLY} == '0' ]] && [[ ${REPLICATION_STATUS[0]} != 'Yes' ]] && [[ ${REPLICATION_STATUS[1]} != 'Yes' ]]; then
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is OK"
		exit 0
	else
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is NOT OK"
		exit 1
	fi
}

check_gr() {
	READ_ONLY=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e 'select @@super_read_only' | /usr/bin/sed -n -e '2p' | /usr/bin/tr -d '\n')
	APPLIER_STATUS=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e "SELECT SERVICE_STATE FROM performance_schema.replication_connection_status WHERE channel_name = 'group_replication_applier'" | /usr/bin/sed -n -e '2p' | /usr/bin/tr -d '\n')

	log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} @@super_read_only: ${READ_ONLY} Applier: ${APPLIER_STATUS}"

	if [[ ${READ_ONLY} == '0' ]] && [[ ${APPLIER_STATUS} == "ON" ]]; then
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is OK"
		exit 0
	else
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is NOT OK"
		exit 1
	fi
}

if [[ ${CLUSTER_TYPE} == "async" ]]; then
	check_async
elif [[ ${CLUSTER_TYPE} == "group-replication" ]]; then
	check_gr
else
	log ERROR "Invalid cluster type: ${CLUSTER_TYPE}"
	exit 1
fi
