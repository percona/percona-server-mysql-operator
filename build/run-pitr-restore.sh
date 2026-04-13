#!/bin/bash

set -e

# TODO: Add support for data at rest encryption

echo "Starting mysqld"
mysqld \
  --admin-address=127.0.0.1 \
  --user=mysql \
  --gtid-mode=ON \
  --enforce-gtid-consistency=ON >/tmp/mysqld.log 2>&1 &

echo "Waiting for mysqld to be ready"
until mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" ping --silent 2>/dev/null; do
    sleep 1;
done

if [[ -n ${SLEEP_FOREVER} ]]; then
  echo "Sleeping forever..."
  touch /var/lib/mysql/sleep-forever
  while [[ -f /var/lib/mysql/sleep-forever ]]; do
    sleep 10
  done
  exit 0
fi

/opt/percona/pitr

echo "Stopping mysqld"
mysqladmin -u operator -p"$(</etc/mysql/mysql-users-secret/operator)" shutdown 2>/dev/null
