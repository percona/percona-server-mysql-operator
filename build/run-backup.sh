#!/bin/bash

set -e

request_backup() {
	curl -s http://${SRC_NODE}:6033/backup/${BACKUP_NAME} -o /backup/${BACKUP_NAME}.stream
}

request_logs() {
	curl -s http://${SRC_NODE}:6033/logs/${BACKUP_NAME}
}

run_s3() {
	cat /backup/${BACKUP_NAME}.stream \
		| xbcloud put ${BACKUP_NAME} --storage=s3 --s3-bucket="${S3_BUCKET}"
}

run_gcs() {
	cat /backup/${BACKUP_NAME}.stream \
		| xbcloud put ${BACKUP_NAME} --storage=google --google-bucket="${GCS_BUCKET}"
}

run_azure() {
	cat /backup/${BACKUP_NAME}.stream | xbcloud put ${BACKUP_NAME} --storage=azure
}

main() {
	echo "Running backup ${BACKUP_NAME} on ${SRC_NODE}"

	if [ ! -f /backup/${BACKUP_NAME}.stream ]; then
		request_backup

		echo "xtrabackup logs:"
		request_logs
	fi

	case ${STORAGE_TYPE} in
		"s3") run_s3 ;;
		"gcs") run_gcs ;;
		"azure") run_azure ;;
	esac
}

main
