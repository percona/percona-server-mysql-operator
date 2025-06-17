#!/bin/bash

trap "exit" SIGTERM

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
TIMEOUT=10
MYSQL_CMDLINE="/usr/bin/timeout 10 /usr/bin/mysql -nNE -u$MYSQL_USER"

for i in {1..5}; do
	CLONE_STATUS=$(MYSQL_PWD=${MYSQL_PASSWORD} $MYSQL_CMDLINE -P$MYSQL_ADMIN_PORT -e 'SELECT STATE FROM performance_schema.clone_status;' | sed -n -e '2p' | tr -d '\n')
	if [ "${CLONE_STATUS}" == 'Completed' -o -z "$CLONE_IN_PROGRESS" ]; then
		echo '[INFO] Started pt-heartbeat'
		break
	else
		echo "[INFO] Waiting for MySQL initialization $((TIMEOUT * i))"
		sleep "$((TIMEOUT * i))"
	fi
done

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
	--password "${HEARTBEAT_PASSWORD}" \
	--port "${MYSQL_ADMIN_PORT}"
