#!/bin/bash

set -o errexit

MYSQL_ADMIN_PORT=33062
MONITOR_USER=monitor
CREDS_MOUNT_PATH=${CREDS_MOUNT_PATH:-/etc/mysql/mysql-users-secret}
TIMEOUT=10

# awk is not in the image's package set, so split with the shell instead.
NODE_IP=${POD_IP:-}
if [ -z "${NODE_IP}" ]; then
	read -r NODE_IP _ <<<"$(hostname -I)"
fi

MYSQL_PWD=$(cat "${CREDS_MOUNT_PATH}/${MONITOR_USER}")
export MYSQL_PWD

# NO_WRITE_TO_BINLOG keeps the flush local: every pod rotates its own logs. The
# log types are named explicitly so the binary log is not rotated along with
# them, which would disrupt binlog retention and PITR.
/usr/bin/timeout "${TIMEOUT}" /usr/bin/mysql -nN \
	-u"${MONITOR_USER}" -h"${NODE_IP}" -P"${MYSQL_ADMIN_PORT}" \
	-e 'FLUSH NO_WRITE_TO_BINLOG ERROR LOGS, SLOW LOGS, GENERAL LOGS;'
