#!/bin/bash

set -e

function log() {
  local ts=$(date +%Y-%m-%dT%H:%M:%S.%N%z --utc | sed 's/+0000/Z/g')
  echo "${ts} 0 [Info] [K8SPS-642] [Job] $*" >&2
}

log "Starting mysqld"
# TODO: Add support for data at rest encryption
mysqld \
  --admin-address=127.0.0.1 \
  --user=mysql \
  --gtid-mode=ON \
  --enforce-gtid-consistency=ON &

log "waiting for mysqld to be ready"
until mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" ping --silent 2>/dev/null; do
    sleep 1;
done
log "mysqld is ready"

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

log "stopping mysqld"
mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" shutdown 2>/dev/null
