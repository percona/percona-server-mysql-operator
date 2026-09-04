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

# Gate the start on authoritative DB state, not on the clone.lock file: native
# InnoDB clone (CLONE INSTANCE) does not reliably create clone.lock, so a
# missing lock is not proof the datadir is ready. Start pt-heartbeat only once
# no clone is 'In Progress' and the sys_operator schema it needs is present -
# a partial clone leaves clone_status='In Progress' with no sys_operator, which
# otherwise crash-loops pt-heartbeat on "Unknown database 'sys_operator'".
#
# Wait for as long as it takes: a clone of a large dataset can run for hours,
# and waiting costs nothing. A genuinely stuck replica is surfaced by the mysql
# container's own readiness, not by this sidecar. We only stop on SIGTERM.
CHECK_INTERVAL=5

while true; do
	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi

	CLONE_STATUS=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT STATE FROM performance_schema.clone_status;' | sed -n -e '2p' | tr -d '\n')
	if [[ $CLONE_STATUS != "In Progress" ]]; then
		HAS_SYS_OPERATOR=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='sys_operator';" | sed -n -e '2p' | tr -d '\n')
		if [[ $HAS_SYS_OPERATOR == "sys_operator" ]]; then
			echo '[INFO] Clone finished and sys_operator present, starting pt-heartbeat'
			break
		fi
	fi

	echo "[INFO] Waiting for clone to finish and sys_operator to appear, clone_status='${CLONE_STATUS:-none}'"

	# Sleep in 1-second intervals to allow signal handling
	for ((j = 0; j < CHECK_INTERVAL; j++)); do
		if [ "$shutdown_requested" -eq 1 ]; then
			exit 0
		fi
		sleep 1
	done
done

# If password contains commas they must be escaped with a backslash: “exam,ple” according https://docs.percona.com/percona-toolkit/pt-heartbeat.html
ESCAPED_HEARTBEAT_PASSWORD="${HEARTBEAT_PASSWORD//,/\\,}"

HEARTBEAT_USER='heartbeat'

# A clone finishes with a mandatory mysqld restart to finalize the data. The
# datadir already looks ready (clone done, sys_operator present) several seconds
# before that restart, so pt-heartbeat can be started just in time to hit it and
# exit with "Server shutdown in progress". Run pt-heartbeat under a bounded retry
# loop: if it exits shortly after starting, wait for MySQL to come back and relaunch
# in-place, so a fresh replica does not show a container restart. A pt-heartbeat that
# ran for a while before exiting is treated as a real failure and surfaced by exiting,
# which lets the container restart as usual.
RETRY_GRACE_SECONDS=60
MAX_QUICK_RETRIES=10
quick_retries=0

while true; do
	if [ "$shutdown_requested" -eq 1 ]; then
		exit 0
	fi

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

	echo "[WARN] pt-heartbeat exited after ${ran_for}s (rc=${rc}); expected around the post-clone MySQL restart - waiting for MySQL and retrying (${quick_retries}/${MAX_QUICK_RETRIES})"

	# Wait for the admin interface to accept connections again before retrying.
	until [ "$shutdown_requested" -eq 1 ] || MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT 1;' >/dev/null 2>&1; do
		sleep 2
	done
done
