#!/bin/bash

set -o errexit
set -o xtrace

OPERATORDIR="/opt/percona-server-mysql-operator"
BINDIR="/opt/percona"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/ps-entrypoint.sh" "${BINDIR}/ps-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/heartbeat-entrypoint.sh" "${BINDIR}/heartbeat-entrypoint.sh"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/orc-entrypoint.sh" "${BINDIR}/orc-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/orchestrator.conf.json" "${BINDIR}/orchestrator.conf.json"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/router-entrypoint.sh" "${BINDIR}/router-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/router_readiness_check.sh" "${BINDIR}/router_readiness_check.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/router_startup_check.sh" "${BINDIR}/router_startup_check.sh"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/bootstrap" "${BINDIR}/bootstrap"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/healthcheck" "${BINDIR}/healthcheck"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/sidecar" "${BINDIR}/sidecar"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/peer-list" "${BINDIR}/peer-list"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/orc-handler" "${BINDIR}/orc-handler"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy-check" "${BINDIR}/haproxy-check"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/run-backup.sh" "${BINDIR}/run-backup.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/run-restore.sh" "${BINDIR}/run-restore.sh"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy-entrypoint.sh" "${BINDIR}/haproxy-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy_add_mysql_nodes.sh" "${BINDIR}/haproxy_add_mysql_nodes.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy_liveness_check.sh" "${BINDIR}/haproxy_liveness_check.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy_readiness_check.sh" "${BINDIR}/haproxy_readiness_check.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy.cfg" "${BINDIR}/haproxy.cfg"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/haproxy-global.cfg" "${BINDIR}/haproxy-global.cfg"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/pmm-prerun.sh" "${BINDIR}/pmm-prerun.sh"