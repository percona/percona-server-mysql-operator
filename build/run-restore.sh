#!/bin/bash

DATADIR=${DATADIR:-/var/lib/mysql}

run_s3() {
	xbcloud get ${BACKUP_NAME} --storage=s3 --s3-bucket=${S3_BUCKET}
}

run_gcs() {
	xbcloud get ${BACKUP_NAME} --storage=google --google-bucket="${GCS_BUCKET}"
}

run_azure() {
	xbcloud get ${BACKUP_NAME} --storage=azure
}

main() {
	echo "Starting restore ${RESTORE_NAME}"
	echo "Restoring to backup ${BACKUP_NAME}"

	rm -rf /var/lib/mysql/*

	case ${STORAGE_TYPE} in
		"s3") run_s3 | xbstream -xv -C ${DATADIR} ;;
		"gcs") run_gcs | xbstream -xv -C ${DATADIR} ;;
		"azure") run_azure | xbstream -xv -C ${DATADIR} ;;
	esac

	echo "Restore finished"
}

main
