#!/bin/bash

set -e

request_data() {
	case "${STORAGE_TYPE}" in
		"s3")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "type": "${STORAGE_TYPE}",
				    "verifyTLS": ${VERIFY_TLS},
				    "s3": {
				        "bucket": "${S3_BUCKET}",
				        "endpointUrl": "${AWS_ENDPOINT}",
				        "accessKey": "${AWS_ACCESS_KEY_ID}",
				        "secretKey": "${AWS_SECRET_ACCESS_KEY}",
				        "region": "${AWS_DEFAULT_REGION}",
				        "storageClass": "${S3_STORAGE_CLASS}"
				    }
				}
			EOF
			;;
		"gcs")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "verifyTLS": ${VERIFY_TLS},
				    "type": "${STORAGE_TYPE}",
				    "gcs": {
				        "bucket": "${GCS_BUCKET}",
				        "endpointUrl": "${GCS_ENDPOINT}",
				        "accessKey": "${ACCESS_KEY_ID}",
				        "secretKey": "${SECRET_ACCESS_KEY}",
				        "storageClass": "${GCS_STORAGE_CLASS}"
				    }
				}
			EOF
			;;
		"azure")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "verifyTLS": ${VERIFY_TLS},
				    "type": "${STORAGE_TYPE}",
				    "azure": {
				        "containerName": "${AZURE_CONTAINER_NAME}",
				        "storageAccount": "${AZURE_STORAGE_ACCOUNT}",
				        "accessKey": "${AZURE_ACCESS_KEY}",
				        "endpointUrl": "${AZURE_ENDPOINT}",
				        "storageClass": "${AZURE_STORAGE_CLASS}"
				    }
				}
			EOF
			;;
	esac
}

request_backup() {
	local sleep_duration=$1
	local http_code

	echo "Trying to run backup ${BACKUP_NAME} on ${SRC_NODE}"
	http_code=$(
		curl -s -o /dev/null \
			-d "$(request_data)" \
			-H "Content-Type: application/json" \
			-w "httpcode=%{http_code}" \
			"http://${SRC_NODE}:6033/backup/${BACKUP_NAME}" \
			| sed -e 's/.*\httpcode=//'
	)
	
	if [ "${http_code}" -eq 200 ]; then
		return
	fi
	if [ "${http_code}" -eq 409 ]; then
		echo "Backup is already running on ${SRC_NODE}"
	else
		echo "Backup failed. Check logs to troubleshoot:"
		echo "kubectl logs ${SRC_NODE%%.*} xtrabackup"
		exit 1
	fi

	echo "Trying again after ${sleep_duration} seconds"
	sleep "${sleep_duration}"
	if [ "${sleep_duration}" -lt 600 ]; then
		sleep_duration=$((sleep_duration * 2))
	fi
	request_backup "${sleep_duration}"
}

request_logs() {
	curl -s http://"${SRC_NODE}":6033/logs/"${BACKUP_NAME}"
}

main() {
	request_backup 10
	request_logs

	echo "Backup finished and uploaded successfully to ${BACKUP_DEST}"
}

main
