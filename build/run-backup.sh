#!/bin/bash

set -e

SIDECAR_PORT="6450"

request_data() {
	case "${STORAGE_TYPE}" in
		"s3")
			cat <<-EOF
				{
				    "destination": "$(json_escape "${BACKUP_DEST}")",
				    "type": "$(json_escape "${STORAGE_TYPE}")",
				    "verifyTLS": $(json_escape "${VERIFY_TLS}"),
				    "s3": {
				        "bucket": "$(json_escape "${S3_BUCKET}")",
				        "endpointUrl": "$(json_escape "${AWS_ENDPOINT}")",
				        "accessKey": "$(json_escape "${AWS_ACCESS_KEY_ID}")",
				        "secretKey": "$(json_escape "${AWS_SECRET_ACCESS_KEY}")",
				        "region": "$(json_escape "${AWS_DEFAULT_REGION}")",
				        "storageClass": "$(json_escape "${S3_STORAGE_CLASS}")"
				    }
				}
			EOF
			;;
		"gcs")
			cat <<-EOF
				{
				    "destination": "$(json_escape "${BACKUP_DEST}")",
				    "verifyTLS": $(json_escape "${VERIFY_TLS}"),
				    "type": "$(json_escape "${STORAGE_TYPE}")",
				    "gcs": {
				        "bucket": "$(json_escape "${GCS_BUCKET}")",
				        "endpointUrl": "$(json_escape "${GCS_ENDPOINT}")",
				        "accessKey": "$(json_escape "${ACCESS_KEY_ID}")",
				        "secretKey": "$(json_escape "${SECRET_ACCESS_KEY}")",
				        "storageClass": "$(json_escape "${GCS_STORAGE_CLASS}")"
				    }
				}
			EOF
			;;
		"azure")
			cat <<-EOF
				{
				    "destination": "$(json_escape "${BACKUP_DEST}")",
				    "verifyTLS": $(json_escape "${VERIFY_TLS}"),
				    "type": "$(json_escape "${STORAGE_TYPE}")",
				    "azure": {
				        "containerName": "$(json_escape "${AZURE_CONTAINER_NAME}")",
				        "storageAccount": "$(json_escape "${AZURE_STORAGE_ACCOUNT}")",
				        "accessKey": "$(json_escape "${AZURE_ACCESS_KEY}")",
				        "endpointUrl": "$(json_escape "${AZURE_ENDPOINT}")",
				        "storageClass": "$(json_escape "${AZURE_STORAGE_CLASS}")"
				    }
				}
			EOF
			;;
	esac
}

# json_escape takes a string and replaces `\` to `\\` and `"` to `\"` to make it safe to insert provided argument into a json string
json_escape() {
	escaped_backslash=${1//'\'/'\\'}
	escaped_quotes=${escaped_backslash//'"'/'\"'}
	echo -n "$escaped_quotes"
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
			"http://${SRC_NODE}:${SIDECAR_PORT}/backup/${BACKUP_NAME}" \
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
	curl -s http://"${SRC_NODE}":${SIDECAR_PORT}/logs/"${BACKUP_NAME}"
}

main() {
	request_backup 10
	request_logs

	echo "Backup finished and uploaded successfully to ${BACKUP_DEST}"
}

main
