#!/bin/bash

set -e

request_data() {
	case "${STORAGE_TYPE}" in
		"s3")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "storage": {
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
				}
			EOF
			;;
		"gcs")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "storage": {
				        "type": "${STORAGE_TYPE}",
				        "verifyTLS": ${VERIFY_TLS},
				        "gcs": {
				            "bucket": "${GCS_BUCKET}",
				            "endpointUrl": "${GCS_ENDPOINT}",
				            "accessKey": "${ACCESS_KEY_ID}",
				            "secretKey": "${SECRET_ACCESS_KEY}",
				            "storageClass": "${GCS_STORAGE_CLASS}"
				        }
				    }
				}
			EOF
			;;
		"azure")
			cat <<-EOF
				{
				    "destination": "${BACKUP_DEST}",
				    "storage": {
				        "type": "${STORAGE_TYPE}",
				        "verifyTLS": ${VERIFY_TLS},
				        "azure": {
				            "containerName": "${AZURE_CONTAINER_NAME}",
				            "storageAccount": "${AZURE_STORAGE_ACCOUNT}",
				            "accessKey": "${AZURE_ACCESS_KEY}",
				            "endpointUrl": "${AZURE_ENDPOINT}",
				            "storageClass": "${AZURE_STORAGE_CLASS}"
				        }
				    }
				}
			EOF
			;;
	esac
}

request_backup() {
	local http_code=$(
		curl \
			-d "$(request_data)" \
			-H "Content-Type: application/json" \
			-w httpcode=%{http_code} \
			"http://${SRC_NODE}:6033/backup/${BACKUP_NAME}" 2>/dev/null \
			| sed -e 's/.*\httpcode=//'
	)

	if [ "${http_code}" -ne 200 ]; then
		echo "Backup failed. Check logs to troubleshoot:"
		echo "kubectl logs ${SRC_NODE%%.*} xtrabackup"
		exit 1
	fi
}

request_logs() {
	curl -s http://"${SRC_NODE}":6033/logs/"${BACKUP_NAME}"
}

main() {
	echo "Running backup ${BACKUP_NAME} on ${SRC_NODE}"

	request_backup
	request_logs

	echo "Backup finished and uploaded successfully to ${BACKUP_DEST}"
}

main
