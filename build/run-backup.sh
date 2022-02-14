#!/bin/bash

set -e
set -o xtrace

XTRABACKUP_USER=${XTRABACKUP_USER:-xtrabackup}
XTRABACKUP_PASSWORD=$(</etc/mysql/mysql-users-secret/$XTRABACKUP_USER)
OPERATOR_USER=${OPERATOR_USER:-operator}
OPERATOR_PASSWORD=$(</etc/mysql/mysql-users-secret/$OPERATOR_USER)

main() {
	until /var/lib/mysql/healthcheck replication; do
		echo "waiting for the replication to become active"
		sleep 5
	done

	xtrabackup --backup=1 \
		--compress \
		--stream=xbstream \
		--user=${XTRABACKUP_USER} \
		--password=${XTRABACKUP_PASSWORD} >/backup/backup.xbstream

	mysql -h 127.0.0.1 -u"${OPERATOR_USER}" -p"${OPERATOR_PASSWORD}" 2>&1 <<-EOSQL
		STOP REPLICA;
		RESET REPLICA ALL;
	EOSQL

	pkill -e -15 mysqld
}

main
