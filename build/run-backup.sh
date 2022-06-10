#!/bin/bash

set -e

XBCLOUD_ARGS="--curl-retriable-errors=7 --parallel=10"
MD5_ARG="--md5"
if [ -n "$VERIFY_TLS" ] && [[ $VERIFY_TLS == "false" ]]; then
	INSECURE_ARG="--insecure"
fi

request_backup() {
	curl -s http://${SRC_NODE}:6033/backup/${BACKUP_NAME} -o /backup/${BACKUP_NAME}.stream
}

request_logs() {
	curl -s http://${SRC_NODE}:6033/logs/${BACKUP_NAME} -o /backup/${BACKUP_NAME}.log
	cat /backup/${BACKUP_NAME}.log
}

run_s3() {
	cat /backup/${BACKUP_NAME}.stream \
		| xbcloud put ${XBCLOUD_ARGS} ${MD5_ARG} ${INSECURE_ARG} --storage=s3 --s3-bucket="${S3_BUCKET}" "${BACKUP_DEST}"
	cat /backup/${BACKUP_NAME}.log \
		| xbcloud put ${XBCLOUD_ARGS} ${INSECURE_ARG} --storage=s3 --s3-bucket="${S3_BUCKET}" "${BACKUP_DEST}.log"
}

run_gcs() {
	cat /backup/${BACKUP_NAME}.stream \
		| xbcloud put ${XBCLOUD_ARGS} ${MD5_ARG} ${INSECURE_ARG} --storage=google --google-bucket="${GCS_BUCKET}" "${BACKUP_DEST}"
	cat /backup/${BACKUP_NAME}.log \
		| xbcloud put ${XBCLOUD_ARGS} ${INSECURE_ARG} --storage=google --google-bucket="${GCS_BUCKET}" "${BACKUP_DEST}.log"
}

run_azure() {
	cat /backup/${BACKUP_NAME}.stream | xbcloud put ${XBCLOUD_ARGS} ${INSECURE_ARG} --storage=azure "${BACKUP_DEST}"
	cat /backup/${BACKUP_NAME}.log | xbcloud put ${XBCLOUD_ARGS} ${INSECURE_ARG} --storage=azure "${BACKUP_DEST}.log"
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

	echo "Backup finished and uploaded successfully to ${BACKUP_DEST}"
}

main
