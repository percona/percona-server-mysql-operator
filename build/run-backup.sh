#!/bin/bash

set -e

XTRABACKUP_USER=${XTRABACKUP_USER:-xtrabackup}
XTRABACKUP_PASSWORD=$(</etc/mysql/mysql-users-secret/$XTRABACKUP_USER)
OPERATOR_USER=${OPERATOR_USER:-operator}
OPERATOR_PASSWORD=$(</etc/mysql/mysql-users-secret/$OPERATOR_USER)

main() {
	if [ ! -n $STORAGE_TYPE ]; then
		echo "STORAGE_TYPE is required"
		exit 1
	fi

	until /var/lib/mysql/healthcheck replication; do
		echo "waiting for the replication to become active"
		sleep 5
	done

	case ${STORAGE_TYPE} in
		"s3") run_s3 ;;
		"gcs") run_gcs ;;
		"azure") run_azure ;;
		"filesystem") run_filesystem ;;
	esac

	mysql -h 127.0.0.1 -u"${OPERATOR_USER}" -p"${OPERATOR_PASSWORD}" 2>&1 <<-EOSQL
		STOP REPLICA;
		RESET REPLICA ALL;
	EOSQL

	pkill -e -15 mysqld
}

get_backup_name() {
	# hostname is FQDN
	echo "${HOSTNAME}" | cut -d '.' -f1
}

run_backup() {
	xtrabackup --backup=1 \
		--compress \
		--stream=xbstream \
		--user=${XTRABACKUP_USER} \
		--password=${XTRABACKUP_PASSWORD}
}

run_s3() {
	run_backup | xbcloud put $(get_backup_name) \
		--storage=s3 \
		--s3-bucket="${S3_BUCKET}"
}

run_gcs() {
	run_backup | xbcloud put $(get_backup_name) \
		--storage=google \
		--google-bucket="${GCS_BUCKET}"
}

run_azure() {
	run_backup | xbcloud put $(get_backup_name) --storage=azure
}

run_filesystem() {
	if [ ! -d /backup ]; then
		echo "not found: /backup"
		exit 1
	fi

	run_backup >/backup/backup.xbstream
}

main
