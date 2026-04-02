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

download() {
	local dest=$1

	case ${STORAGE_TYPE} in
		"s3")
			# shellcheck disable=SC2086
			xbcloud get ${XBCLOUD_ARGS} "${dest}" --storage=s3 --s3-bucket="${S3_BUCKET}"
			;;
		"gcs")
			# shellcheck disable=SC2086
			xbcloud get ${XBCLOUD_ARGS} "${dest}" --storage=google --google-bucket="${GCS_BUCKET}"
			;;
		"azure")
			# shellcheck disable=SC2086
			xbcloud get ${XBCLOUD_ARGS} "${dest}" --storage=azure
			;;
		*)
			echo "Error: unknown storage type: ${STORAGE_TYPE}"
			exit 1
			;;
	esac
}

extract() {
	local targetdir=$1

	# shellcheck disable=SC2086
	xbstream -xv -C "${targetdir}" --parallel="${PARALLEL}" ${XBSTREAM_EXTRA_ARGS}
}

get_keyring_arg() {
	local keyring=""
	if [[ -f ${KEYRING_VAULT_PATH} ]]; then
		if [[ ${XTRABACKUP_VERSION} == "8.0" ]]; then
			echo "Using keyring vault config: ${KEYRING_VAULT_PATH}" >&2
			keyring="--keyring-vault-config=${KEYRING_VAULT_PATH}"
		elif [[ ${XTRABACKUP_VERSION} == "8.4" ]]; then
			cp "${KEYRING_VAULT_PATH}" /tmp/component_keyring_vault.cnf
			echo "Using keyring vault component: /tmp/component_keyring_vault.cnf" >&2
			keyring="--component-keyring-config=/tmp/component_keyring_vault.cnf"
		fi
	fi
	echo "${keyring}"
}

restore_full() {
	echo "Starting full restore ${RESTORE_NAME}"
	echo "Restoring to backup: ${BACKUP_DEST}"

	rm -rf "${DATADIR:?}"/*
	tmpdir=$(mktemp --directory "${DATADIR}/${RESTORE_NAME}_XXXX")

	download "${BACKUP_DEST}" | extract "${tmpdir}"

	local keyring
	keyring=$(get_keyring_arg)

	# shellcheck disable=SC2086
	xtrabackup --prepare --rollback-prepared-trx --target-dir="${tmpdir}" ${XB_EXTRA_ARGS} ${keyring}
	# shellcheck disable=SC2086
	xtrabackup --datadir="${DATADIR}" --move-back --force-non-empty-directories --target-dir="${tmpdir}" ${XB_EXTRA_ARGS}

	rm -rf "${tmpdir}"

	echo "Full restore finished"
}

restore_incremental() {
	echo "Starting incremental restore ${RESTORE_NAME}"
	echo "Base backup: ${BACKUP_DEST}"
	echo "Incrementals: ${BACKUP_INCREMENTALS_DEST}"

	rm -rf "${DATADIR:?}"/*
	local basedir
	basedir=$(mktemp --directory "${DATADIR}/${RESTORE_NAME}_base_XXXX")

	# Download and extract the base (full) backup
	echo "Downloading base backup: ${BACKUP_DEST}"
	download "${BACKUP_DEST}" | extract "${basedir}"

	local keyring
	keyring=$(get_keyring_arg)

	# Prepare the base backup with --apply-log-only (redo only, no rollback)
	# shellcheck disable=SC2086
	xtrabackup --prepare --apply-log-only --target-dir="${basedir}" ${XB_EXTRA_ARGS} ${keyring}

	# Parse the comma-separated list of incremental destinations
	IFS=',' read -ra INCR_DESTS <<< "${BACKUP_INCREMENTALS_DEST}"
	local total=${#INCR_DESTS[@]}
	local count=0

	for incr_dest in "${INCR_DESTS[@]}"; do
		count=$((count + 1))
		echo "Downloading incremental backup ${count}/${total}: ${incr_dest}"

		local incrdir
		incrdir=$(mktemp --directory "${DATADIR}/${RESTORE_NAME}_incr${count}_XXXX")

		download "${incr_dest}" | extract "${incrdir}"

		if [ "${count}" -lt "${total}" ]; then
			# Not the last incremental: use --apply-log-only
			# shellcheck disable=SC2086
			xtrabackup --prepare --apply-log-only --target-dir="${basedir}" --incremental-dir="${incrdir}" ${XB_EXTRA_ARGS} ${keyring}
		else
			# Last incremental: omit --apply-log-only to allow rollback of uncommitted transactions
			# shellcheck disable=SC2086
			xtrabackup --prepare --target-dir="${basedir}" --incremental-dir="${incrdir}" ${XB_EXTRA_ARGS} ${keyring}
		fi

		rm -rf "${incrdir}"
	done

	# Move the prepared backup to the data directory
	# shellcheck disable=SC2086
	xtrabackup --datadir="${DATADIR}" --move-back --force-non-empty-directories --target-dir="${basedir}" ${XB_EXTRA_ARGS}

	rm -rf "${basedir}"

	echo "Incremental restore finished"
}

main() {
	if [ -n "${BACKUP_INCREMENTALS_DEST}" ]; then
		restore_incremental
	else
		restore_full
	fi
}

main
