#!/bin/bash

shutdown_requested=0
hb_pid=''

handle_shutdown() {
	shutdown_requested=1
	echo '[INFO] Shutdown requested, exiting heartbeat entrypoint'
	# Forward the signal to a running pt-heartbeat so the container stops promptly.
	if [ -n "$hb_pid" ] && kill -0 "$hb_pid" 2>/dev/null; then
		kill -TERM "$hb_pid" 2>/dev/null
	fi
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
MYSQL_CMDLINE="/usr/bin/timeout 10 /usr/bin/mysql -nNE -u$MYSQL_USER"
CHECK_INTERVAL=5

# wait_for_datadir_ready blocks until it is safe to run pt-heartbeat: no clone is
# active and the sys_operator schema it needs is present. It is gated on
# authoritative DB state, not the clone.lock file, because native InnoDB clone
# (CLONE INSTANCE) does not reliably create clone.lock.
#
# A clone is considered active for any non-terminal clone_status STATE (mirroring
# CloneInProgress in the operator): only an empty status (no clone, e.g. the
# primary) or 'Completed' means no clone is running. It also requires sys_operator,
# because a fresh replica has sys_operator created during initialization and only
# starts CLONE INSTANCE afterwards - the clone then drops and recreates the schema.
#
# It waits for as long as it takes (a large clone can run for hours); a genuinely
# stuck replica is surfaced by the mysql container's own readiness. Returns 0 when
# ready, non-zero on shutdown.
wait_for_datadir_ready() {
	while true; do
		if [ "$shutdown_requested" -eq 1 ]; then
			return 1
		fi

		CLONE_STATUS=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT STATE FROM performance_schema.clone_status;' | sed -n -e '2p' | tr -d '\n')
		if [[ -z $CLONE_STATUS || $CLONE_STATUS == "Completed" ]]; then
			HAS_SYS_OPERATOR=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='sys_operator';" | sed -n -e '2p' | tr -d '\n')
			if [[ $HAS_SYS_OPERATOR == "sys_operator" ]]; then
				return 0
			fi
		fi

		echo "[INFO] Waiting for clone to finish and sys_operator to appear, clone_status='${CLONE_STATUS:-none}'"

		# Sleep in 1-second intervals to allow signal handling
		for ((j = 0; j < CHECK_INTERVAL; j++)); do
			if [ "$shutdown_requested" -eq 1 ]; then
				return 1
			fi
			sleep 1
		done
	done
}

# If password contains commas they must be escaped with a backslash: “exam,ple” according https://docs.percona.com/percona-toolkit/pt-heartbeat.html
ESCAPED_HEARTBEAT_PASSWORD="${HEARTBEAT_PASSWORD//,/\\,}"

HEARTBEAT_USER='heartbeat'

# Run pt-heartbeat under a bounded retry loop, re-checking the datadir before
# every (re)launch. This covers two cases without ever restarting the container:
#   - a clone that starts just after we pass the gate (a fresh replica creates
#     sys_operator, then CLONE INSTANCE drops it): pt-heartbeat exits, and the
#     next iteration waits the clone out before relaunching.
#   - the mandatory mysqld restart at the end of a clone: pt-heartbeat exits with
#     "Server shutdown in progress", and we relaunch once MySQL is back.
# A pt-heartbeat that ran for a while before exiting is treated as a real failure
# and surfaced by exiting, which lets the container restart as usual.
RETRY_GRACE_SECONDS=60
MAX_QUICK_RETRIES=10
quick_retries=0

while true; do
	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi

	if ! wait_for_datadir_ready; then
		exit 0
	fi

	echo "[INFO] Clone finished and sys_operator present, starting pt-heartbeat"
	echo "[INFO] pt-heartbeat --update --replace --fail-successive-errors 20 --check-read-only --create-table --database sys_operator \
		--table heartbeat --user ${HEARTBEAT_USER} --password XXXX --port ${MYSQL_ADMIN_PORT}"

	start_ts=$SECONDS
	pt-heartbeat \
		--update \
		--replace \
		--fail-successive-errors 20 \
		--check-read-only \
		--create-table \
		--database sys_operator \
		--table heartbeat \
		--user "${HEARTBEAT_USER}" \
		--password "${ESCAPED_HEARTBEAT_PASSWORD}" \
		--port "${MYSQL_ADMIN_PORT}" &
	hb_pid=$!
	wait "$hb_pid"
	rc=$?
	hb_pid=''
	ran_for=$((SECONDS - start_ts))

	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi

	if [ "$ran_for" -ge "$RETRY_GRACE_SECONDS" ]; then
		# Ran long enough to be considered healthy before exiting: a real failure.
		# Exit so the container restarts and the problem is visible.
		echo "[ERROR] pt-heartbeat exited after ${ran_for}s (rc=${rc}); exiting so the container restarts"
		exit "$rc"
	fi

	quick_retries=$((quick_retries + 1))
	if [ "$quick_retries" -gt "$MAX_QUICK_RETRIES" ]; then
		echo "[ERROR] pt-heartbeat kept exiting quickly (${quick_retries} times, last rc=${rc}); giving up so the container restarts"
		exit "$rc"
	fi

	echo "[WARN] pt-heartbeat exited after ${ran_for}s (rc=${rc}); re-checking datadir and retrying (${quick_retries}/${MAX_QUICK_RETRIES})"
done
