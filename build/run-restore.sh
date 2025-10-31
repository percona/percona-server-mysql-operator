#!/bin/bash

set -e
set -o xtrace

XTRABACKUP_VERSION=$(xtrabackup --version 2>&1 | awk '/^xtrabackup version/{print $3}' | awk -F'.' '{print $1"."$2}')
DATADIR=${DATADIR:-/var/lib/mysql}
PARALLEL=$(grep -c processor /proc/cpuinfo)
XBCLOUD_ARGS="--curl-retriable-errors=7 --parallel=${PARALLEL} ${XBCLOUD_EXTRA_ARGS}"
if [ -n "$VERIFY_TLS" ] && [[ $VERIFY_TLS == "false" ]]; then
	XBCLOUD_ARGS="${XBCLOUD_ARGS} --insecure"
fi

run_s3() {
	xbcloud get ${XBCLOUD_ARGS} "${BACKUP_DEST}" --storage=s3 --s3-bucket="${S3_BUCKET}"
}

run_gcs() {
	xbcloud get ${XBCLOUD_ARGS} "${BACKUP_DEST}" --storage=google --google-bucket="${GCS_BUCKET}"
}

run_azure() {
	xbcloud get ${XBCLOUD_ARGS} "${BACKUP_DEST}" --storage=azure
}

extract() {
	local targetdir=$1

	# shellcheck disable=SC2086
	xbstream -xv -C "${targetdir}" --parallel="${PARALLEL}" ${XBSTREAM_EXTRA_ARGS}
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

	local keyring=""
	if [[ -f ${KEYRING_VAULT_PATH} ]]; then
		if [[ ${XTRABACKUP_VERSION} == "8.0" ]]; then
			echo "Using keyring vault config: ${KEYRING_VAULT_PATH}"
			keyring="--keyring-vault-config=${KEYRING_VAULT_PATH}"
		elif [[ ${XTRABACKUP_VERSION} == "8.4" ]]; then
			# PXB expects the config with a specific name
			cp ${KEYRING_VAULT_PATH} /tmp/component_keyring_vault.cnf
			echo "Using keyring vault component: /tmp/component_keyring_vault.cnf"
			keyring="--component-keyring-config=/tmp/component_keyring_vault.cnf"
		fi
	fi

	# shellcheck disable=SC2086
	xtrabackup --prepare --rollback-prepared-trx --target-dir="${tmpdir}" ${XB_EXTRA_ARGS} ${keyring}
	# shellcheck disable=SC2086
	xtrabackup --datadir="${DATADIR}" --move-back --force-non-empty-directories --target-dir="${tmpdir}" ${XB_EXTRA_ARGS}

	rm -rf "${tmpdir}"

	echo "Restore finished"
}

main
