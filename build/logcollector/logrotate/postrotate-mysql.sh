#!/bin/bash

set -o errexit

MYSQL_ADMIN_PORT=33062
MONITOR_USER=monitor
CREDS_MOUNT_PATH=${CREDS_MOUNT_PATH:-/etc/mysql/mysql-users-secret}
AUDIT_COMPONENT_URN='file://component_audit_log_filter'
TIMEOUT=10

# awk is not in the image's package set, so split with the shell instead.
NODE_IP=${POD_IP:-}
if [ -z "${NODE_IP}" ]; then
	read -r NODE_IP _ <<<"$(hostname -I)"
fi

MYSQL_PWD=$(cat "${CREDS_MOUNT_PATH}/${MONITOR_USER}")
export MYSQL_PWD

mysql_exec() {
	/usr/bin/timeout "${TIMEOUT}" /usr/bin/mysql -nN \
		-u"${MONITOR_USER}" -h"${NODE_IP}" -P"${MYSQL_ADMIN_PORT}" "$@"
}

# NO_WRITE_TO_BINLOG keeps it local; naming the types spares the binary log.
mysql_exec -e 'FLUSH NO_WRITE_TO_BINLOG ERROR LOGS, SLOW LOGS, GENERAL LOGS;'

# FLUSH does not reopen the audit log, so ask it to rotate itself.
# PS 8.4+ has the component; 8.0 still ships the audit_log plugin.
audit_component=$(mysql_exec -e "SELECT COUNT(*) FROM mysql.component WHERE component_urn = '${AUDIT_COMPONENT_URN}'" 2>/dev/null || echo 0)
audit_plugin=$(mysql_exec -e 'SHOW PLUGINS' 2>/dev/null | grep -c 'audit_log' || true)
if [ "${audit_component:-0}" -gt 0 ]; then
	mysql_exec -e 'DO audit_log_rotate();' || echo "WARNING: audit log rotation failed" >&2
elif [ "${audit_plugin:-0}" -gt 0 ]; then
	mysql_exec -e 'SET GLOBAL audit_log_flush=1;' || echo "WARNING: audit log flush failed" >&2
fi
