#!/bin/bash
set -e

export PATH="$PATH:/opt/fluent-bit/bin"

LOGCOLLECTOR_DIR="/opt/percona/logcollector"

# The UID the log collector image ships with, and the /etc/passwd entry rewritten
# when the platform overrides it.
NOMINAL_UID=1001

run_cron() {
	local schedule="$1"
	local cmd="$2"

	if [ -f /usr/bin/supercronic ]; then
		printf '%s %s\n' "$schedule" "$cmd" >/tmp/crontab
		exec supercronic /tmp/crontab
	else
		exec go-cron "$schedule" sh -c "$cmd"
	fi
}

# logrotate resolves the running UID through /etc/passwd. OpenShift's restricted
# SCC ignores the image's runAsUser and assigns a random UID from the namespace
# range, which has no passwd entry, so point the image's own entry at it.
ensure_passwd_entry() {
	if [[ $EUID == "$NOMINAL_UID" ]] || grep -q "^[^:]*:[^:]*:${EUID}:" /etc/passwd; then
		return 0
	fi

	if [[ ! -w /etc/passwd ]]; then
		echo "WARNING: running as UID $EUID with no /etc/passwd entry, and /etc/passwd is not writable; logrotate may fail"
		return 0
	fi

	sed -e "s|x:${NOMINAL_UID}:|x:${EUID}:|" /etc/passwd >/tmp/passwd
	cat /tmp/passwd >/etc/passwd
	rm -f /tmp/passwd
}

is_logrotate_config_invalid() {
	local config_file="$1"
	if [ -z "$config_file" ] || [ ! -f "$config_file" ]; then
		return 1
	fi
	# Specifying -d runs in debug mode, so even in case of errors, it will exit with 0.
	# We need to check the output for "error" but skip those lines that are related to the missing logrotate.status file.
	# Filter out logrotate.status lines first, then check for remaining errors
	(
		set +e
		logrotate -d "$config_file" 2>&1 | grep -v "logrotate.status" | grep -qiE "^error:"
	)
	return $?
}

run_logrotate() {
	local logrotate_status_file="${LOG_DIR}/logrotate.status"
	local logrotate_conf_file="${LOGCOLLECTOR_DIR}/logrotate/logrotate.conf"
	local logrotate_additional_conf_files=()
	local conf_d_dir="${LOGCOLLECTOR_DIR}/logrotate/conf.d"

	ensure_passwd_entry

	mkdir -p "${LOG_DIR}"

	# Operator-managed mysql.conf overrides the default when present.
	if [ -f "$conf_d_dir/mysql.conf" ]; then
		logrotate_conf_file="$conf_d_dir/mysql.conf"
		if is_logrotate_config_invalid "$logrotate_conf_file"; then
			echo "ERROR: Logrotate configuration is invalid, fallback to default configuration"
			logrotate_conf_file="${LOGCOLLECTOR_DIR}/logrotate/logrotate.conf"
		fi
	fi

	# Process all other .conf files under conf.d (mysql.conf handled above).
	if [ -d "$conf_d_dir" ]; then
		for conf_file in "$conf_d_dir"/*.conf; do
			[ -f "$conf_file" ] || continue
			[ "$(basename "$conf_file")" = "mysql.conf" ] && continue
			if is_logrotate_config_invalid "$conf_file"; then
				echo "ERROR: Logrotate configuration file $conf_file is invalid, it will be ignored"
			else
				logrotate_additional_conf_files+=("$conf_file")
			fi
		done
	fi

	local logrotate_cmd="logrotate -s \"$logrotate_status_file\" \"$logrotate_conf_file\""
	for additional_conf in "${logrotate_additional_conf_files[@]}"; do
		logrotate_cmd="$logrotate_cmd \"$additional_conf\""
	done

	set -o xtrace
	run_cron "$LOGROTATE_SCHEDULE" "$logrotate_cmd"
}

run_fluentbit() {
	local fluentbit_opt=(-c "${LOGCOLLECTOR_DIR}/fluentbit/fluentbit.yaml")
	mkdir -p /tmp/fluentbit/custom
	mkdir -p "${LOG_DIR}/fluentbit"

	# The tail database format changes between Fluent Bit major versions; keying
	# the file on the version stops an upgraded collector from reading a database
	# it cannot parse (which would make it re-send every log line from scratch).
	FLB_DB_VER="$(fluent-bit --version 2>/dev/null | grep -oE 'v[0-9]+' | head -1)"
	export FLB_DB_VER="${FLB_DB_VER:-v0}"

	set +e
	local fluentbit_conf_dir="${LOGCOLLECTOR_DIR}/fluentbit/custom"
	for conf_file in "$fluentbit_conf_dir"/*.yaml; do
		[ -f "$conf_file" ] || continue
		if ! fluent-bit --dry-run -c "$conf_file" >/dev/null 2>&1; then
			echo "ERROR: Fluentbit configuration file $conf_file is invalid, it will be ignored"
		else
			cp "$conf_file" /tmp/fluentbit/custom/
		fi
	done
	touch /tmp/fluentbit/custom/default.yaml || true

	set -e
	set -o xtrace
	exec "$@" "${fluentbit_opt[@]}"
}

case "$1" in
	logrotate)
		run_logrotate
		;;
	fluent-bit)
		run_fluentbit "$@"
		;;
	*)
		echo "Invalid argument: $1"
		exit 1
		;;
esac
