#!/bin/bash

DATA_DIR='/var/lib/mysql'
until [ ! -f "$DATA_DIR/bootstrap.lock" ] && [ ! -f "$DATA_DIR/clone.lock" ] && [ -S "$DATA_DIR/mysql.sock" ]; do
	echo '[INFO] Waiting for MySQL initialization ...'
	sleep 10
done

# wait until bootstrap start clone process
# we can get situation when ps-entrypoint removed bootstrap.lock but bootstrap has not created clone.lock yet
sleep 10
if [ -f /var/lib/mysql/clone.lock ]; then
	CLONE_IN_PROGRESS='yes'
fi

MYSQL_ADMIN_PORT='33062'
MYSQL_USER="${MYSQL_USERNAME:-monitor}"
MYSQL_PASSWORD=$(cat /etc/mysql/mysql-users-secret/monitor || :)
TIMEOUT="${CLONE_TIMEOUT_SECONDS:-3600}"
MYSQL_CMDLINE="/usr/bin/timeout 10 /usr/bin/mysql -nNE -u$MYSQL_USER"

# Check clone status every 5 seconds up to TIMEOUT
CHECK_INTERVAL=5
ELAPSED=0

while [ $ELAPSED -lt $TIMEOUT ]; do
	CLONE_STATUS=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT STATE FROM performance_schema.clone_status;' | sed -n -e '2p' | tr -d '\n')
	if [[ $CLONE_STATUS == "Completed" || -z $CLONE_IN_PROGRESS ]]; then
		echo '[INFO] Clone completed, starting pt-heartbeat'
		break
	fi

	echo "[INFO] Waiting for MySQL initialization (${ELAPSED}s/${TIMEOUT}s)"

	# Sleep in 1-second intervals to allow signal handling
	for ((j=0; j<CHECK_INTERVAL; j++)); do
		sleep 1
	done

	ELAPSED=$((ELAPSED + CHECK_INTERVAL))
done

# If password contains commas they must be escaped with a backslash: “exam,ple” according https://docs.percona.com/percona-toolkit/pt-heartbeat.html
ESCAPED_HEARTBEAT_PASSWORD="${HEARTBEAT_PASSWORD//,/\\,}"

HEARTBEAT_USER='heartbeat'
echo "[INFO] pt-heartbeat --update --replace --fail-successive-errors 20 --check-read-only --create-table --database sys_operator \
	--table heartbeat --user ${HEARTBEAT_USER} --password XXXX --port ${MYSQL_ADMIN_PORT}"

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
	--port "${MYSQL_ADMIN_PORT}"
