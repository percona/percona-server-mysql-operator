#!/bin/bash

set -o xtrace

until [ ! -f /var/lib/mysql/bootstrap.lock ] && [ ! -f /var/lib/mysql/clone.lock ]; do
	echo "waiting for MySQL initialization"
	sleep 10
done

sleep 60

echo "Started pt-heartbeat"
pt-heartbeat \
	--update \
	--replace \
	--fail-successive-errors 20 \
	--check-read-only \
	--create-table \
	--database sys_operator \
	--table heartbeat \
	--user heartbeat \
	--password "${HEARTBEAT_PASSWORD}" \
	--port 33062
