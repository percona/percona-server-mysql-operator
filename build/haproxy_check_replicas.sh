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
MYSQL_CMDLINE="/usr/bin/timeout $TIMEOUT /usr/bin/mysql -BnN -u${MONITOR_USER} -h ${MYSQL_SERVER_IP} -P ${MYSQL_SERVER_PORT}"

CLUSTER_TYPE=$(/bin/cat /tmp/cluster_type)

check_async() {
	local VALUES=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e "select concat(concat(@@global.read_only,',', @@global.super_read_only));select service_state from performance_schema.replication_connection_status where channel_name='';select service_state from performance_schema.replication_applier_status where channel_name='';")

	local REPLICATION_STATUS=($(echo $VALUES | /bin/tr "," "\n"))
	local READ_ONLY=${REPLICATION_STATUS[0]}
	local SUPER_RO=${REPLICATION_STATUS[1]}
	local REP_IO_STATUS=${REPLICATION_STATUS[2]}
	local REP_SQL_STATUS=${REPLICATION_STATUS[3]}

	log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} Super_Read_Only: ${SUPER_RO} Read_Only: ${READ_ONLY} Replica_IO_Running: ${REP_IO_STATUS} Replica_SQL_Running: ${REP_SQL_STATUS}"

	if [[ ${SUPER_RO} == '1' ]] && [[ ${READ_ONLY} == '1' ]] && [[ "${REP_IO_STATUS}" == 'ON' ]] && [[ "${REP_SQL_STATUS}" == 'ON' ]]; then
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is OK"
		exit 0
	else
		log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is NOT OK"
		exit 1
	fi
}

check_gr() {
	local VALUES=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e "select concat(concat(@@global.read_only,',', @@global.super_read_only),',',(select MEMBER_STATE from performance_schema.replication_group_members where MEMBER_ID = @@global.server_uuid ));")

	local REPLICATION_STATUS=($(echo $VALUES | /bin/tr "," "\n"))
	local READ_ONLY=${REPLICATION_STATUS[0]}
	local SUPER_RO=${REPLICATION_STATUS[1]}
	local NODE_STATUS=${REPLICATION_STATUS[2]}

	log INFO "${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} Super_Read_Only: ${SUPER_RO} Read_Only: ${READ_ONLY} Node_Status: ${NODE_STATUS}"

	if [[ ${SUPER_RO} == '1' ]] && [[ ${READ_ONLY} == '1' ]] && [[ ${NODE_STATUS} == "ONLINE" ]]; then
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
