#!/bin/bash

set -e

function mysql_exec() {
  mysql -uoperator -p"$(</etc/mysql/mysql-users-secret/operator)" -N -e "$1"
}

function log() {
  local ts=$(date +%Y-%m-%dT%H:%M:%S.%N%z --utc | sed 's/+0000/Z/g')
  echo "${ts} 0 [Info][Job] $*" >&2
}

function cleanup_clusterset_meta() {
  local has_cs_table cs_rows=0
  has_cs_table=$(mysql_exec "SELECT COUNT(*) FROM information_schema.tables
          WHERE table_schema='mysql_innodb_cluster_metadata' AND table_name='clustersets';")
  if [ "${has_cs_table}" != "0" ]; then
    cs_rows=$(mysql_exec "SELECT COUNT(*) FROM mysql_innodb_cluster_metadata.clustersets;")
  fi
  if [ "${cs_rows}" == "0" ]; then
    log "No ClusterSet metadata detected; leaving restored data untouched"
    return
  fi
  log "ClusterSet metadata detected, cleaning up restored data"

  mysql_exec "SELECT group_replication_reset_member_actions();"
  mysql_exec "SET GLOBAL super_read_only = OFF; SET GLOBAL read_only = OFF; STOP REPLICA; RESET REPLICA ALL; RESET PERSIST;"
}

function start_mysqld() {
  log "Starting mysqld"
  mysqld \
    --loose-group-replication-start-on-boot=OFF \
    --skip-replica-start=ON \
    --plugin-load-add=group_replication.so \
    --gtid-mode=ON \
    --enforce-gtid-consistency=ON \
    --skip-networking --user=mysql &
  MYSQLD_PID=$!

  log "Waiting for mysqld to be ready"
  for i in {60..0}; do
    if ! kill -0 "${MYSQLD_PID}" 2>/dev/null; then
      log "mysqld exited during startup"
      wait "${MYSQLD_PID}" || true
      exit 1
    fi
    mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" ping --silent 2>/dev/null && break
    sleep 1
  done
  [ "$i" -eq 0 ] && {
    log "timed out waiting for mysqld"
    kill -s TERM "${MYSQLD_PID}" 2>/dev/null
    exit 1
  }
  log "mysqld is ready"
}

function stop_mysqld() {
  log "Stopping mysqld"
  mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" shutdown 2>/dev/null
}

start_mysqld
cleanup_clusterset_meta
stop_mysqld
