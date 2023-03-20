#!/bin/bash
set -eo pipefail
shopt -s nullglob
set -o xtrace

# if command starts with an option, prepend mysqld
if [ "${1:0:1}" = '-' ]; then
	set -- mysqld "$@"
fi

# skip setup if they want an option that stops mysqld
wantHelp=
for arg; do
	case "$arg" in
		-'?' | --help | --print-defaults | -V | --version)
			wantHelp=1
			break
			;;
	esac
done

# usage: file_env VAR [DEFAULT]
#    ie: file_env 'XYZ_DB_PASSWORD' 'example'
# (will allow for "$XYZ_DB_PASSWORD_FILE" to fill in the value of
#  "$XYZ_DB_PASSWORD" from a file, especially for Docker's secrets feature)
file_env() {
	set +o xtrace
	local var="$1"
	local fileVar="${var}_FILE"
	local def="${2:-}"
	if [ "${!var:-}" ] && [ "${!fileVar:-}" ]; then
		echo >&2 "error: both $var and $fileVar are set (but are exclusive)"
		exit 1
	fi
	local val="$def"
	if [ "${!var:-}" ]; then
		val="${!var}"
	elif [ "${!fileVar:-}" ]; then
		val="$(<"${!fileVar}")"
	elif [ "${3:-}" ] && [ -f "/etc/mysql/mysql-users-secret/$3" ]; then
		val="$(</etc/mysql/mysql-users-secret/"$3")"
	fi
	export "$var"="$val"
	unset "$fileVar"
	set -o xtrace
}

# usage: process_init_file FILENAME MYSQLCOMMAND...
#    ie: process_init_file foo.sh mysql -uroot
# (process a single initializer file, based on its extension. we define this
# function here, so that initializer scripts (*.sh) can use the same logic,
# potentially recursively, or override the logic used in subsequent calls)
process_init_file() {
	local f="$1"
	shift
	local mysql=("$@")

	case "$f" in
		*.sh)
			echo "$0: running $f"
			. "$f"
			;;
		*.sql)
			echo "$0: running $f"
			"${mysql[@]}" <"$f"
			echo
			;;
		*.sql.gz)
			echo "$0: running $f"
			gunzip -c "$f" | "${mysql[@]}"
			echo
			;;
		*) echo "$0: ignoring $f" ;;
	esac
	echo
}

_check_config() {
	toRun=("$@" --verbose --help)
	if ! errors="$("${toRun[@]}" 2>&1 >/dev/null)"; then
		cat >&2 <<-EOM

			ERROR: mysqld failed while attempting to check config
			command was: "${toRun[*]}"

			$errors
		EOM
		exit 1
	fi
}

# Fetch value from server config
# We use mysqld --verbose --help instead of my_print_defaults because the
# latter only show values present in config files, and not server defaults
_get_config() {
	local conf="$1"
	shift
	"$@" --verbose --help --log-bin-index="$(mktemp -u)" 2>/dev/null \
		| awk '$1 == "'"$conf"'" && /^[^ \t]/ { sub(/^[^ \t]+[ \t]+/, ""); print; exit }'
	# match "datadir      /some/path with/spaces in/it here" but not "--xyz=abc\n     datadir (xyz)"
}

# Fetch value from customized configs, needed for non-mysqld options like sst
_get_cnf_config() {
	local group=$1
	local var=${2//_/-}
	local reval=""

	reval=$(
		my_print_defaults "${group}" \
			| awk -F= '{st=index($0,"="); cur=$0; if ($1 ~ /_/) { gsub(/_/,"-",$1);} if (st != 0) { print $1"="substr(cur,st+1) } else { print cur }}' \
			| grep -- "--$var=" \
			| cut -d= -f2- \
			| tail -1
	)

	if [[ -z $reval ]]; then
		reval=$3
	fi
	echo "$reval"
}

_get_tmpdir() {
	local defaul_value="$1"
	local tmpdir_path=""

	tmpdir_path=$(_get_cnf_config mysqld tmpdir "")
	if [[ -z ${tmpdir_path} ]]; then
		tmpdir_path=$(_get_cnf_config xtrabackup tmpdir "")
	fi
	if [[ -z ${tmpdir_path} ]]; then
		tmpdir_path="$defaul_value"
	fi
	echo "$tmpdir_path"
}

CFG=/etc/my.cnf.d/node.cnf
TLS_DIR=/etc/mysql/mysql-tls-secret
CUSTOM_CONFIG_FILES=("/etc/mysql/config/auto-config.cnf" "/etc/mysql/config/my-config.cnf" "/etc/mysql/config/my-secret.cnf")

create_default_cnf() {
	POD_IP=$(hostname -I | awk '{print $1}')

	if [[ ${HOSTNAME} =~ "-xb-" ]]; then
		FQDN=${HOSTNAME}
	else
		CLUSTER_NAME="$(hostname -f | cut -d'.' -f2)"
		SERVER_NUM=${HOSTNAME/$CLUSTER_NAME-/}
		SERVER_ID=${CLUSTER_HASH}${SERVER_NUM}
		FQDN="${HOSTNAME}.${SERVICE_NAME}.$(</var/run/secrets/kubernetes.io/serviceaccount/namespace)"
	fi

	echo '[mysqld]' >$CFG
	sed -i "/\[mysqld\]/a read_only=ON" $CFG
	sed -i "/\[mysqld\]/a server_id=${SERVER_ID}" $CFG
	sed -i "/\[mysqld\]/a admin-address=${POD_IP}" $CFG
	sed -i "/\[mysqld\]/a report_host=${FQDN}" $CFG
	sed -i "/\[mysqld\]/a report_port=3306" $CFG
	sed -i "/\[mysqld\]/a gtid-mode=ON" $CFG
	sed -i "/\[mysqld\]/a enforce-gtid-consistency=ON" $CFG
	sed -i "/\[mysqld\]/a log_error_verbosity=3" $CFG
	sed -i "/\[mysqld\]/a plugin-load-add=clone=mysql_clone.so" $CFG
	sed -i "/\[mysqld\]/a plugin-load-add=rpl_semi_sync_master=semisync_master.so" $CFG
	sed -i "/\[mysqld\]/a plugin-load-add=rpl_semi_sync_slave=semisync_slave.so" $CFG

	if [[ -d ${TLS_DIR} ]]; then
		sed -i "/\[mysqld\]/a ssl_ca=${TLS_DIR}/ca.crt" $CFG
		sed -i "/\[mysqld\]/a ssl_cert=${TLS_DIR}/tls.crt" $CFG
		sed -i "/\[mysqld\]/a ssl_key=${TLS_DIR}/tls.key" $CFG
	fi

	for f in "${CUSTOM_CONFIG_FILES[@]}"; do
		echo "${f}"
		if [ -f "${f}" ]; then
			(
				cat "${f}"
				echo
			) >>$CFG
		fi
	done
}

load_group_replication_plugin() {
	POD_IP=$(hostname -I | awk '{print $1}')

	sed -i "/\[mysqld\]/a plugin_load_add=group_replication.so" $CFG
}

MYSQL_VERSION=$(mysqld -V | awk '{print $3}' | awk -F'.' '{print $1"."$2}')

if [ "$MYSQL_VERSION" != '8.0' ]; then
	echo "Percona Distribution for MySQL Operator does not support $MYSQL_VERSION"
	exit 1
fi

if [ "$1" = 'mysqld' -a -z "$wantHelp" ]; then
	# still need to check config, container may have started with --user
	_check_config "$@"

	# Get config
	DATADIR="$(_get_config 'datadir' "$@")"
	TMPDIR=$(_get_tmpdir "$DATADIR/mysql-tmpdir")

	rm -rfv "$TMPDIR"

	create_default_cnf

	if [ ! -d "$DATADIR/mysql" ]; then
		touch /var/lib/mysql/bootstrap.lock
		file_env 'MYSQL_ROOT_PASSWORD' '' 'root'
		{ set +x; } 2>/dev/null
		if [ -z "$MYSQL_ROOT_PASSWORD" -a -z "$MYSQL_ALLOW_EMPTY_PASSWORD" -a -z "$MYSQL_RANDOM_ROOT_PASSWORD" ]; then
			echo >&2 'error: database is uninitialized and password option is not specified '
			echo >&2 '  You need to specify one of MYSQL_ROOT_PASSWORD, MYSQL_ALLOW_EMPTY_PASSWORD and MYSQL_RANDOM_ROOT_PASSWORD'
			exit 1
		fi
		set -x

		mkdir -p "$DATADIR"
		find "$DATADIR" -mindepth 1 -prune -o -exec rm -rfv {} \+ 1>/dev/null

		echo 'Initializing database'
		# we initialize database into $TMPDIR because "--initialize-insecure" option does not work if directory is not empty
		# in some cases storage driver creates unremovable artifacts (see K8SPXC-286), so $DATADIR cleanup is not possible
		"$@" --initialize-insecure --skip-ssl --datadir="$TMPDIR"
		mv "$TMPDIR"/* "$DATADIR/"
		rm -rfv "$TMPDIR"
		echo 'Database initialized'

		SOCKET="$(_get_config 'socket' "$@")"
		"$@" --skip-networking --socket="${SOCKET}" &
		pid="$!"

		mysql=(mysql --protocol=socket -uroot -hlocalhost --socket="${SOCKET}" --password="")

		for i in {120..0}; do
			if echo 'SELECT 1' | "${mysql[@]}" &>/dev/null; then
				break
			fi
			echo 'MySQL init process in progress...'
			sleep 1
		done
		if [ "$i" = 0 ]; then
			echo >&2 'MySQL init process failed.'
			exit 1
		fi

		if [ -z "$MYSQL_INITDB_SKIP_TZINFO" ]; then
			(
				echo "SET @@SESSION.SQL_LOG_BIN = off;"
				# sed is for https://bugs.mysql.com/bug.php?id=20545
				mysql_tzinfo_to_sql /usr/share/zoneinfo | sed 's/Local time zone must be set--see zic manual page/FCTY/'
			) | "${mysql[@]}" mysql
		fi

		{ set +x; } 2>/dev/null
		if [ -n "$INIT_ROCKSDB" ]; then
			ps-admin --docker --enable-rocksdb -u root -p "$MYSQL_ROOT_PASSWORD"
		fi

		if [ -n "$MYSQL_RANDOM_ROOT_PASSWORD" ]; then
			MYSQL_ROOT_PASSWORD="$(pwmake 128)"
			echo "GENERATED ROOT PASSWORD: $MYSQL_ROOT_PASSWORD"
		fi
		set -x

		rootCreate=
		# default root to listen for connections from anywhere
		file_env 'MYSQL_ROOT_HOST' '%'
		if [ -n "$MYSQL_ROOT_HOST" -a "$MYSQL_ROOT_HOST" != 'localhost' ]; then
			# no, we don't care if read finds a terminating character in this heredoc
			# https://unix.stackexchange.com/questions/265149/why-is-set-o-errexit-breaking-this-read-heredoc-expression/265151#265151
			read -r -d '' rootCreate <<-EOSQL || true
				CREATE USER 'root'@'${MYSQL_ROOT_HOST}' IDENTIFIED BY '${MYSQL_ROOT_PASSWORD}' ;
				GRANT ALL ON *.* TO 'root'@'${MYSQL_ROOT_HOST}' WITH GRANT OPTION ;
			EOSQL
		fi

		file_env 'MONITOR_HOST' 'localhost'
		file_env 'MONITOR_PASSWORD' 'monitor' 'monitor'
		file_env 'XTRABACKUP_PASSWORD' '' 'xtrabackup'
		file_env 'REPLICATION_PASSWORD' '' 'replication'
		file_env 'ORC_TOPOLOGY_PASSWORD' '' 'orchestrator'
		file_env 'OPERATOR_ADMIN_PASSWORD' '' 'operator'
		file_env 'XTRABACKUP_PASSWORD' '' 'xtrabackup'
		file_env 'HEARTBEAT_PASSWORD' '' 'heartbeat'
		read -r -d '' monitorConnectGrant <<-EOSQL || true
			GRANT SERVICE_CONNECTION_ADMIN ON *.* TO 'monitor'@'${MONITOR_HOST}';
		EOSQL
		"${mysql[@]}" <<-EOSQL
			-- What's done in this file shouldn't be replicated
			--  or products like mysql-fabric won't work
			SET @@SESSION.SQL_LOG_BIN=0;

			DELETE FROM mysql.user WHERE user NOT IN ('mysql.sys', 'mysqlxsys', 'root', 'mysql.infoschema', 'mysql.session') OR host NOT IN ('localhost') ;
			ALTER USER 'root'@'localhost' IDENTIFIED BY '${MYSQL_ROOT_PASSWORD}' ;
			GRANT ALL ON *.* TO 'root'@'localhost' WITH GRANT OPTION ;
			${rootCreate}
			/*!80016 REVOKE SYSTEM_USER ON *.* FROM root */;

			CREATE USER 'operator'@'${MYSQL_ROOT_HOST}' IDENTIFIED BY '${OPERATOR_ADMIN_PASSWORD}' ;
			GRANT ALL ON *.* TO 'operator'@'${MYSQL_ROOT_HOST}' WITH GRANT OPTION ;

			CREATE USER 'xtrabackup'@'localhost' IDENTIFIED BY '${XTRABACKUP_PASSWORD}';
			GRANT SYSTEM_USER, BACKUP_ADMIN, PROCESS, RELOAD, GROUP_REPLICATION_ADMIN, REPLICATION_SLAVE_ADMIN, LOCK TABLES, REPLICATION CLIENT ON *.* TO 'xtrabackup'@'localhost';
			GRANT SELECT ON performance_schema.replication_group_members TO 'xtrabackup'@'localhost';
			GRANT SELECT ON performance_schema.log_status TO 'xtrabackup'@'localhost';
			GRANT SELECT ON performance_schema.keyring_component_status TO 'xtrabackup'@'localhost';

			CREATE USER 'monitor'@'${MONITOR_HOST}' IDENTIFIED BY '${MONITOR_PASSWORD}' WITH MAX_USER_CONNECTIONS 100;
			GRANT SYSTEM_USER, SELECT, PROCESS, SUPER, REPLICATION CLIENT, RELOAD, BACKUP_ADMIN ON *.* TO 'monitor'@'${MONITOR_HOST}';
			GRANT SELECT ON performance_schema.* TO 'monitor'@'${MONITOR_HOST}';
			${monitorConnectGrant}

			CREATE USER 'replication'@'%' IDENTIFIED BY '${REPLICATION_PASSWORD}';
			GRANT DELETE, INSERT, UPDATE ON mysql.* TO 'replication'@'%' WITH GRANT OPTION;
			GRANT SELECT ON performance_schema.threads to 'replication'@'%';
			GRANT SYSTEM_USER, REPLICATION SLAVE, BACKUP_ADMIN, GROUP_REPLICATION_STREAM, CLONE_ADMIN, CONNECTION_ADMIN, CREATE USER, EXECUTE, FILE, GROUP_REPLICATION_ADMIN, PERSIST_RO_VARIABLES_ADMIN, PROCESS, RELOAD, REPLICATION CLIENT, REPLICATION_APPLIER, REPLICATION_SLAVE_ADMIN, ROLE_ADMIN, SELECT, SHUTDOWN, SYSTEM_VARIABLES_ADMIN ON *.* TO 'replication'@'%' WITH GRANT OPTION;
			GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata.* TO 'replication'@'%' WITH GRANT OPTION;
			GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_bkp.* TO 'replication'@'%' WITH GRANT OPTION;
			GRANT ALTER, ALTER ROUTINE, CREATE, CREATE ROUTINE, CREATE TEMPORARY TABLES, CREATE VIEW, DELETE, DROP, EVENT, EXECUTE, INDEX, INSERT, LOCK TABLES, REFERENCES, SHOW VIEW, TRIGGER, UPDATE ON mysql_innodb_cluster_metadata_previous.* TO 'replication'@'%' WITH GRANT OPTION;

			CREATE USER 'orchestrator'@'%' IDENTIFIED BY '${ORC_TOPOLOGY_PASSWORD}';
			GRANT SYSTEM_USER, SUPER, PROCESS, REPLICATION SLAVE, REPLICATION CLIENT, RELOAD ON *.* TO 'orchestrator'@'%';
			GRANT SELECT ON mysql.slave_master_info TO 'orchestrator'@'%';
			GRANT SELECT ON sys_operator.* TO 'orchestrator'@'%';

			CREATE DATABASE IF NOT EXISTS sys_operator;
			CREATE USER 'heartbeat'@'localhost' IDENTIFIED BY '${HEARTBEAT_PASSWORD}';
			GRANT SYSTEM_USER, REPLICATION CLIENT ON *.* TO 'heartbeat'@'localhost';
			GRANT SELECT, CREATE, DELETE, UPDATE, INSERT ON sys_operator.heartbeat TO 'heartbeat'@'localhost';

			DROP DATABASE IF EXISTS test;
			FLUSH PRIVILEGES ;
		EOSQL

		{ set +x; } 2>/dev/null
		if [ -n "$MYSQL_ROOT_PASSWORD" ]; then
			mysql+=(-p"${MYSQL_ROOT_PASSWORD}")
		fi
		set -x

		file_env 'MYSQL_DATABASE'
		if [ "$MYSQL_DATABASE" ]; then
			echo "CREATE DATABASE IF NOT EXISTS \`$MYSQL_DATABASE\` ;" | "${mysql[@]}"
			mysql+=("$MYSQL_DATABASE")
		fi

		file_env 'MYSQL_USER'
		file_env 'MYSQL_PASSWORD'
		{ set +x; } 2>/dev/null
		if [ "$MYSQL_USER" -a "$MYSQL_PASSWORD" ]; then
			echo "CREATE USER '$MYSQL_USER'@'%' IDENTIFIED BY '$MYSQL_PASSWORD' ;" | "${mysql[@]}"

			if [ "$MYSQL_DATABASE" ]; then
				echo "GRANT ALL ON \`$MYSQL_DATABASE\`.* TO '$MYSQL_USER'@'%' ;" | "${mysql[@]}"
			fi

			echo 'FLUSH PRIVILEGES ;' | "${mysql[@]}"
		fi
		set -x

		echo
		ls /docker-entrypoint-initdb.d/ >/dev/null
		for f in /docker-entrypoint-initdb.d/*; do
			process_init_file "$f" "${mysql[@]}"
		done

		{ set +x; } 2>/dev/null
		if [ -n "$MYSQL_ONETIME_PASSWORD" ]; then
			"${mysql[@]}" <<-EOSQL
				ALTER USER 'root'@'%' PASSWORD EXPIRE;
			EOSQL
		fi
		set -x
		if ! kill -s TERM "$pid" || ! wait "$pid"; then
			echo >&2 'MySQL init process failed.'
			exit 1
		fi

		rm /var/lib/mysql/bootstrap.lock
		echo
		echo 'MySQL init process done. Ready for start up.'
		echo
	fi

	load_group_replication_plugin

	# exit when MYSQL_INIT_ONLY environment variable is set to avoid starting mysqld
	if [ -n "$MYSQL_INIT_ONLY" ]; then
		echo 'Initialization complete, now exiting!'
		exit 0
	fi
fi

if [[ -f /var/lib/mysql/full-cluster-crash ]]; then
	set +o xtrace
	node_name=$(hostname -f)
	cluster_name=$(hostname | cut -d '-' -f1) # TODO: This won't work if CR has `-` in its name.
	gtid_executed=$(</var/lib/mysql/full-cluster-crash)
	namespace=$(</var/run/secrets/kubernetes.io/serviceaccount/namespace)

	echo "######FULL_CLUSTER_CRASH:${node_name}######"
	echo "You have full cluster crash. You need to recover the cluster manually. Here are the steps:"
	echo ""
	echo "Latest GTID_EXECUTED in this node is ${gtid_executed}"
	echo "Compare GTIDs in each MySQL pod and select the one with the newest GTID."
	echo ""
	echo "Create /var/lib/mysql/force-bootstrap inside the mysql container. For example, if you select ${cluster_name}-mysql-2 to recover from:"
	echo "$ kubectl -n ${namespace} exec ${cluster_name}-mysql-2 -c mysql -- touch /var/lib/mysql/force-bootstrap"
	echo ""
	echo "Remove /var/lib/mysql/full-cluster-crash in this pod to re-bootstrap the group. For example:"
	echo "$ kubectl -n ${namespace} exec ${cluster_name}-mysql-2 -c mysql -- rm /var/lib/mysql/full-cluster-crash"
	echo "This will restart the mysql container."
	echo ""
	echo "After group is bootstrapped and mysql container is ready, move on to the other pods:"
	echo "$ kubectl -n ${namespace} exec ${cluster_name}-mysql-1 -c mysql -- rm /var/lib/mysql/full-cluster-crash"
	echo "Wait until the pod ready"
	echo ""
	echo "$ kubectl -n ${namespace} exec ${cluster_name}-mysql-0 -c mysql -- rm /var/lib/mysql/full-cluster-crash"
	echo "Wait until the pod ready"
	echo ""
	echo "Continue to other pods if you have more."
	echo "#####LAST_LINE:${node_name}:${gtid_executed}"

	for (( ; ; )); do
		if [[ ! -f /var/lib/mysql/full-cluster-crash ]]; then
			exit 0
		fi
		sleep 5
	done
fi

exec "$@"
