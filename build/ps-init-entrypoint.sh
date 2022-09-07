#!/bin/bash

set -o errexit
set -o xtrace

BINDIR="${BINDIR}"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/ps-entrypoint.sh" "${BINDIR}/ps-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/heartbeat-entrypoint.sh" "${BINDIR}/heartbeat-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/router-entrypoint.sh" "${BINDIR}/router-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/orc-entrypoint.sh" "${BINDIR}/orc-entrypoint.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/orchestrator.conf.json" "${BINDIR}/orchestrator.conf.json"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/bootstrap" "${BINDIR}/bootstrap"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/healthcheck" "${BINDIR}/healthcheck"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/sidecar" "${BINDIR}/sidecar"

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/run-backup.sh" "${BINDIR}/run-backup.sh"
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D "${BINDIR}/run-restore.sh" "${BINDIR}/run-restore.sh"
