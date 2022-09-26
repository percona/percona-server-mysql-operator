#!/bin/bash

set -e

MYSQL_SERVER_IP=$3
MYSQL_SERVER_PORT='33062'

MONITOR_USER='monitor'
MONITOR_PASSWORD=$(/bin/cat /etc/mysql/mysql-users-secret/monitor)

TIMEOUT=${CUSTOM_TIMEOUT:-10}
MYSQL_CMDLINE="/usr/bin/timeout $TIMEOUT /usr/bin/mysql -nNE -u${MONITOR_USER} -h ${MYSQL_SERVER_IP} -P ${MYSQL_SERVER_PORT}"

READ_ONLY=$(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e 'select @@super_read_only' | /usr/bin/sed -n -e '2p' | /usr/bin/tr -d '\n')

# ${REPLICATION_STATUS[0]} - Replica_IO_Running
# ${REPLICATION_STATUS[1]} - Replica_SQL_Running
REPLICATION_STATUS=($(MYSQL_PWD="${MONITOR_PASSWORD}" ${MYSQL_CMDLINE} -e 'SHOW REPLICA STATUS' | /usr/bin/sed -n -e '12p' -e '13p' | /usr/bin/tr '\n' ' '))

echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT}"
echo "read_only: ${READ_ONLY}"
echo "Replica_IO_Running: ${REPLICATION_STATUS[0]}"
echo "Replica_SQL_Running: ${REPLICATION_STATUS[1]}"

if [[ ${READ_ONLY} == '0' ]] && [[ ${REPLICATION_STATUS[0]} != 'Yes' ]] && [[ ${REPLICATION_STATUS[1]} != 'Yes' ]]; then
	echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is ok"
	exit 0
else
	echo "MySQL node ${MYSQL_SERVER_IP}:${MYSQL_SERVER_PORT} for backend ${HAPROXY_PROXY_NAME} is not ok"
	exit 1
fi
