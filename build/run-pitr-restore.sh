#!/bin/bash

set -e

LIB_PATH='/opt/percona/lib'
# shellcheck source=build/lib/util.sh
. ${LIB_PATH}/util.sh

LOG_PREFIX="[K8SPS-642]"

trap remove_keyring_component EXIT

configure_keyring

start_mysqld \
	--admin-address=127.0.0.1 \
	--gtid-mode=ON \
	--enforce-gtid-consistency=ON

if [[ -n ${SLEEP_FOREVER} ]]; then
	SLEEP_FOREVER_FILE=/var/lib/mysql/sleep-forever
	log "sleeping forever... remove ${SLEEP_FOREVER_FILE} to terminate."
	touch ${SLEEP_FOREVER_FILE}
	while [[ -f ${SLEEP_FOREVER_FILE} ]]; do
		sleep 10
	done
	exit 0
fi

log "starting recovery"
/opt/percona/pitr

stop_mysqld
