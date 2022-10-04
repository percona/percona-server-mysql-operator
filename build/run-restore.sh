#!/bin/bash

set -e
set -o xtrace

DATADIR=${DATADIR:-/var/lib/mysql}
PARALLEL=$(grep -c processor /proc/cpuinfo)
XBCLOUD_ARGS="--curl-retriable-errors=7 --parallel=${PARALLEL}"
if [ -n "$VERIFY_TLS" ] && [[ $VERIFY_TLS == "false" ]]; then
	XBCLOUD_ARGS="${XBCLOUD_ARGS} --insecure"
fi

run_s3() {
	xbcloud get "${XBCLOUD_ARGS}" "${BACKUP_DEST}" --storage=s3 --s3-bucket="${S3_BUCKET}"
}

run_gcs() {
	xbcloud get "${XBCLOUD_ARGS}" "${BACKUP_DEST}" --storage=google --google-bucket="${GCS_BUCKET}"
}

run_azure() {
	xbcloud get "${XBCLOUD_ARGS}" "${BACKUP_DEST}" --storage=azure
}

extract() {
	local targetdir=$1

	xbstream -xv -C "${targetdir}" --parallel="${PARALLEL}"
}

main() {
	echo "Starting restore ${RESTORE_NAME}"
	echo "Restoring to backup: ${BACKUP_DEST}"

	rm -rf "${DATADIR:?}"/*
	tmpdir=$(mktemp --directory "${DATADIR}/${RESTORE_NAME}_XXXX")

	case ${STORAGE_TYPE} in
		"s3") run_s3 | extract "${tmpdir}" ;;
		"gcs") run_gcs | extract "${tmpdir}" ;;
		"azure") run_azure | extract "${tmpdir}" ;;
	esac

	xtrabackup --prepare --rollback-prepared-trx --target-dir="${tmpdir}"
	xtrabackup --datadir="${DATADIR}" --move-back --force-non-empty-directories --target-dir="${tmpdir}"

	rm -rf "${tmpdir}"

	echo "Restore finished"
}

main
