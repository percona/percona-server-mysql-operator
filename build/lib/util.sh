#!/bin/bash
# Helpers shared by the scripts that configure or start a mysqld: the node
# entrypoint and the restore jobs. Meant to be sourced, not executed.

DATADIR=${DATADIR:-/var/lib/mysql}
OPERATOR_PASS_FILE=${OPERATOR_PASS_FILE:-/etc/mysql/mysql-users-secret/operator}
MYSQLD_STARTUP_TIMEOUT=${MYSQLD_STARTUP_TIMEOUT:-600}

# Set by configure_keyring, consumed by start_mysqld.
KEYRING_ARGS=()
ENCRYPTION_ARGS=()
KEYRING_COMPONENT_INSTALLED=0

log() {
	local ts
	ts=$(date +%Y-%m-%dT%H:%M:%S.%N%z --utc | sed 's/+0000/Z/g')
	echo "${ts} 0 [Info] ${LOG_PREFIX:+${LOG_PREFIX} }[Job] $*" >&2
}

mysql_version() {
	if [ -z "${MYSQL_VERSION}" ]; then
		MYSQL_VERSION=$(mysqld -V | awk '{print $3}' | awk -F'.' '{print $1"."$2}')
	fi
	echo "${MYSQL_VERSION}"
}

mysql_exec() {
	mysql -uoperator -p"$(<"${OPERATOR_PASS_FILE}")" -N -e "$1"
}

mysqladmin_exec() {
	mysqladmin -u operator -p"$(<"${OPERATOR_PASS_FILE}")" "$@"
}

# The vault secret is mounted optionally, so its absence is how we detect that
# the cluster runs without data at rest encryption.
keyring_enabled() {
	[[ -n ${KEYRING_VAULT_PATH} && -f ${KEYRING_VAULT_PATH} ]]
}

install_keyring_component() {
	echo -n '{ "components": "file://component_keyring_vault" }' >"${DATADIR}/mysqld.my"
	cp "${KEYRING_VAULT_PATH}" "${DATADIR}/component_keyring_vault.cnf"
	KEYRING_COMPONENT_INSTALLED=1
}

uninstall_keyring_component() {
	rm -f "${DATADIR}/mysqld.my" "${DATADIR}/component_keyring_vault.cnf"
	KEYRING_COMPONENT_INSTALLED=0
}

# EXIT trap for the jobs that install the component themselves: the datadir
# they touch is the one the cluster starts from afterwards, so it must be left
# exactly as it was found.
remove_keyring_component() {
	if [ "${KEYRING_COMPONENT_INSTALLED}" -eq 0 ]; then
		return
	fi
	log "Removing keyring vault component from ${DATADIR}"
	uninstall_keyring_component
}

# The settings that make an encrypted cluster encrypt everything it writes:
# tables, undo/redo logs, binlogs and temporary files. A mysqld working on a
# restored datadir needs the same set, otherwise whatever it writes lands
# unencrypted next to the encrypted data it was started on.
mysql_encryption_options() {
	cat <<-EOF
		default_table_encryption=ON
		table_encryption_privilege_check=ON
		innodb_undo_log_encrypt=ON
		innodb_redo_log_encrypt=ON
		binlog_encryption=ON
		binlog_rotate_encryption_master_key_at_startup=ON
		innodb_temp_tablespace_encrypt=ON
		innodb_encrypt_online_alter_logs=ON
		encrypt_tmp_files=ON
	EOF

	# this variable causes mysqld to crash in 8.4
	if [ "$(mysql_version)" == '8.0' ]; then
		echo "innodb_parallel_dblwr_encrypt=ON"
	fi
}

# Without the keyring, mysqld can't open the encrypted tablespaces in the
# datadir and refuses to start.
configure_keyring() {
	KEYRING_ARGS=()
	ENCRYPTION_ARGS=()

	if ! keyring_enabled; then
		return
	fi

	if [ "$(mysql_version)" == '8.0' ]; then
		log "Using keyring vault plugin: ${KEYRING_VAULT_PATH}"
		KEYRING_ARGS=(
			--early-plugin-load=keyring_vault.so
			--keyring_vault_config="${KEYRING_VAULT_PATH}"
		)
	else
		# 8.4 and newer dropped the plugin in favor of a component, which is
		# loaded through a manifest in the datadir. ps-entrypoint.sh installs
		# these files on every mysqld start, so we drop ours once we're done
		# with them.
		log "Using keyring vault component: ${DATADIR}/component_keyring_vault.cnf"
		install_keyring_component
	fi

	local opt
	while read -r opt; do
		ENCRYPTION_ARGS+=("--${opt}")
	done < <(mysql_encryption_options)
}

# Starts mysqld in the background with the keyring and encryption settings from
# configure_keyring, and waits for it to accept connections. Any extra argument
# is passed to mysqld as-is.
start_mysqld() {
	log "Starting mysqld"
	mysqld \
		"${KEYRING_ARGS[@]}" \
		"${ENCRYPTION_ARGS[@]}" \
		--datadir="${DATADIR}" \
		--user=mysql \
		"$@" &
	MYSQLD_PID=$!

	log "Waiting for mysqld to be ready"
	local i
	for ((i = MYSQLD_STARTUP_TIMEOUT; i > 0; i--)); do
		if ! kill -0 "${MYSQLD_PID}" 2>/dev/null; then
			log "mysqld exited during startup"
			wait "${MYSQLD_PID}" || true
			exit 1
		fi
		mysqladmin_exec ping --silent 2>/dev/null && break
		sleep 1
	done
	[ "$i" -eq 0 ] && {
		log "timed out waiting for mysqld after ${MYSQLD_STARTUP_TIMEOUT}s"
		kill -s TERM "${MYSQLD_PID}" 2>/dev/null
		exit 1
	}
	log "mysqld is ready"
}

stop_mysqld() {
	log "Stopping mysqld"
	mysqladmin_exec shutdown 2>/dev/null
}
