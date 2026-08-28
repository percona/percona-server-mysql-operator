#!/bin/bash

shutdown_requested=0

handle_shutdown() {
	shutdown_requested=1
	echo '[INFO] Shutdown requested, exiting heartbeat entrypoint'
}

trap handle_shutdown SIGTERM SIGINT

DATA_DIR='/var/lib/mysql'
until [ ! -f "$DATA_DIR/bootstrap.lock" ] && [ ! -f "$DATA_DIR/clone.lock" ] && [ -S "$DATA_DIR/mysql.sock" ]; do
	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi
	echo '[INFO] Waiting for MySQL initialization ...'
	sleep 10
done

if [ "$shutdown_requested" -eq 1 ]; then
	exit 0
fi

MYSQL_ADMIN_PORT='33062'
MYSQL_USER="${MYSQL_USERNAME:-monitor}"
MYSQL_PASSWORD=$(cat /etc/mysql/mysql-users-secret/monitor || :)
TIMEOUT="${CLONE_TIMEOUT_SECONDS:-3600}"
MYSQL_CMDLINE="/usr/bin/timeout 10 /usr/bin/mysql -nNE -u$MYSQL_USER"

# Gate the start on authoritative DB state, not on the clone.lock file: native
# InnoDB clone (CLONE INSTANCE) does not reliably create clone.lock, so a
# missing lock is not proof the datadir is ready. Start pt-heartbeat only once
# no clone is 'In Progress' and the sys_operator schema it needs is present -
# a partial clone leaves clone_status='In Progress' with no sys_operator, which
# otherwise crash-loops pt-heartbeat on "Unknown database 'sys_operator'".
CHECK_INTERVAL=5
ELAPSED=0
READY=''

while [ "$ELAPSED" -lt "$TIMEOUT" ]; do
	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi

	CLONE_STATUS=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT STATE FROM performance_schema.clone_status;' | sed -n -e '2p' | tr -d '\n')
	if [[ $CLONE_STATUS != "In Progress" ]]; then
		HAS_SYS_OPERATOR=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='sys_operator';" | sed -n -e '2p' | tr -d '\n')
		if [[ $HAS_SYS_OPERATOR == "sys_operator" ]]; then
			echo '[INFO] Clone finished and sys_operator present, starting pt-heartbeat'
			READY='yes'
			break
		fi
	fi

	echo "[INFO] Waiting for clone to finish and sys_operator to appear (${ELAPSED}s/${TIMEOUT}s), clone_status='${CLONE_STATUS:-none}'"

	# Sleep in 1-second intervals to allow signal handling
	for ((j = 0; j < CHECK_INTERVAL; j++)); do
		if [ "$shutdown_requested" -eq 1 ]; then
			exit 0
		fi
		sleep 1
	done

	ELAPSED=$((ELAPSED + CHECK_INTERVAL))
done

if [ -z "$READY" ]; then
	echo "[ERROR] datadir not ready after ${TIMEOUT}s (clone still in progress or sys_operator missing); refusing to start pt-heartbeat against an incomplete datadir"
	exit 1
fi

# If password contains commas they must be escaped with a backslash: “exam,ple” according https://docs.percona.com/percona-toolkit/pt-heartbeat.html
ESCAPED_HEARTBEAT_PASSWORD="${HEARTBEAT_PASSWORD//,/\\,}"

HEARTBEAT_USER='heartbeat'
echo "[INFO] pt-heartbeat --update --replace --fail-successive-errors 20 --check-read-only --create-table --database sys_operator \
	--table heartbeat --user ${HEARTBEAT_USER} --password XXXX --port ${MYSQL_ADMIN_PORT}"

exec pt-heartbeat \
	--update \
	--replace \
	--fail-successive-errors 20 \
	--check-read-only \
	--create-table \
	--database sys_operator \
	--table heartbeat \
	--user "${HEARTBEAT_USER}" \
	--password "${ESCAPED_HEARTBEAT_PASSWORD}" \
	--port "${MYSQL_ADMIN_PORT}"
