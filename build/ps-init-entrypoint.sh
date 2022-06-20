#!/bin/bash

set -o errexit
set -o xtrace

install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /ps-entrypoint.sh /var/lib/mysql/ps-entrypoint.sh
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /bootstrap /var/lib/mysql/bootstrap
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /healthcheck /var/lib/mysql/healthcheck
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /sidecar /var/lib/mysql/sidecar
install -o "$(id -u)" -g "$(id -g)" -m 0755 -D /run-backup.sh /var/lib/mysql/run-backup.sh
