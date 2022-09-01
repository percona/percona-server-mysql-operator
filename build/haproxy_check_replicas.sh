#!/bin/bash

set -e
set -o xtrace

MYSQL_SERVER_IP=$3
MYSQL_SERVER_PORT='33062'

MONITOR_USER='monitor'
MONITOR_PASSWORD=$(/bin/cat /etc/mysql/mysql-users-secret/monitor)

TIMEOUT=${CUSTOM_TIMEOUT:-10}
MYSQL_CMDLINE="/usr/bin/timeout $TIMEOUT /usr/bin/mysql -nE -u${MONITOR_USER} -p${MONITOR_PASSWORD} -h ${MYSQL_SERVER_IP} -P ${MYSQL_SERVER_PORT}"

READ_ONLY=$(${MYSQL_CMDLINE} -N -e 'select @@read_only and @@super_read_only' | tail -1)
IO_THREAD=$(${MYSQL_CMDLINE} -e 'SHOW REPLICA STATUS' | grep Replica_IO_Running | tail -1 | awk '{print $2}')
SQL_THREAD=$(${MYSQL_CMDLINE} -e 'SHOW REPLICA STATUS' | grep Replica_SQL_Running | head -1 | awk '{print $2}')

echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT}"
echo "read_only: ${READ_ONLY}"
echo "Replica_IO_Running: ${IO_THREAD}"
echo "Replica_SQL_Running: ${SQL_THREAD}"

if [[ ${READ_ONLY} == "1" ]] && [[ ${IO_THREAD} == "Yes" ]] && [[ ${SQL_THREAD} == "Yes" ]]; then
	echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is ok"
	exit 0
else
	echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is not ok"
	exit 1
fi
