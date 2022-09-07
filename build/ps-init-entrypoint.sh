#!/bin/bash

set -o errexit
set -o xtrace

OPERATORDIR="/opt/percona-server-mysql-operator"
BINDIR="/opt/percona"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/ps-entrypoint.sh" "${BINDIR}/ps-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/heartbeat-entrypoint.sh" "${BINDIR}/heartbeat-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/router-entrypoint.sh" "${BINDIR}/router-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/orc-entrypoint.sh" "${BINDIR}/orc-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/orchestrator.conf.json" "${BINDIR}/orchestrator.conf.json"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/bootstrap" "${BINDIR}/bootstrap"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/healthcheck" "${BINDIR}/healthcheck"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/sidecar" "${BINDIR}/sidecar"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/run-backup.sh" "${BINDIR}/run-backup.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${OPERATORDIR}/run-restore.sh" "${BINDIR}/run-restore.sh"
