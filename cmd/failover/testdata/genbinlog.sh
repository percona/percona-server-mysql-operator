#!/bin/bash
# Generates testdata/replica-<version>[-<variant>].binlog from a replica: replicated transactions (original commit ts
# differs), a local transaction, small/medium/large transactions, DDL, an XA transaction, and a clean-shutdown Stop event.
# TAGGED=1 (8.4+) adds tagged transactions: small and large with an automatic GTID, one with an explicit GTID, and a local one.
set -euo pipefail
# Usage: genbinlog.sh <percona-server version> <output file> [mysqld option...]. The options go to both servers, e.g.
# --binlog-transaction-compression=ON or --binlog-checksum=NONE. mysqlbinlog -vv of the result goes to stdout.
v=$1 out=$2
shift 2
src=gtid-src-$v rep=gtid-rep-$v
common=(--gtid-mode=ON --enforce-gtid-consistency=ON --log-bin=binlog --skip-name-resolve "$@")
docker rm -f $src $rep >/dev/null 2>&1 || true
docker run -d --name $src --network host -e MYSQL_ROOT_PASSWORD=pw percona/percona-server:$v "${common[@]}" --server-id=1 --port=34306 --mysqlx=OFF >/dev/null
docker run -d --name $rep --network host -e MYSQL_ROOT_PASSWORD=pw percona/percona-server:$v "${common[@]}" --server-id=2 --port=34307 --mysqlx=OFF >/dev/null
q() { docker exec $1 mysql -uroot -ppw -h127.0.0.1 -P$2 -N -e "$3" 2>/dev/null; }
for c in "$src 34306" "$rep 34307"; do
  set -- $c
  for i in $(seq 120); do q $1 $2 "select 1" >/dev/null && break; sleep 1; done
done
q $src 34306 "set sql_log_bin=0; create user repl@'%' identified with mysql_native_password by 'pw'; grant replication slave on *.* to repl@'%'" 2>/dev/null || \
q $src 34306 "set sql_log_bin=0; create user repl@'%' identified by 'pw'; grant replication slave on *.* to repl@'%'"
q $rep 34307 "change replication source to source_host='127.0.0.1', source_port=34306, source_user='repl', source_password='pw', source_auto_position=1, get_source_public_key=1; start replica"
q $rep 34307 "flush binary logs"
q $src 34306 "create database t; create table t.a (id int primary key auto_increment, v longtext)"
q $src 34306 "insert into t.a (v) values ('small')"
q $src 34306 "insert into t.a (v) values (repeat('m', 1000))"
q $src 34306 "insert into t.a (v) values (repeat('l', 70000))"
q $src 34306 "begin; insert into t.a (v) values ('x'); insert into t.a (v) values ('y'); commit"
q $src 34306 "xa start 'x'; insert into t.a (v) values ('xa'); xa end 'x'; xa prepare 'x'; xa commit 'x'"
rows=6
if [ "${TAGGED:-}" = 1 ]; then
  q $src 34306 "set gtid_next='AUTOMATIC:orders'; insert into t.a (v) values ('tagged')"
  q $src 34306 "set gtid_next='AUTOMATIC:orders'; insert into t.a (v) values (repeat('t', 70000))"
  q $src 34306 "set gtid_next='11111111-1111-1111-1111-111111111111:billing:7'; insert into t.a (v) values ('explicit')"
  rows=9
fi
for i in $(seq 60); do
  [ "$(q $rep 34307 "select count(*) from t.a" || true)" = $rows ] && break; sleep 1
done
q $rep 34307 "insert into t.a (v) values ('local')"
[ "${TAGGED:-}" = 1 ] && q $rep 34307 "set gtid_next='AUTOMATIC:local'; insert into t.a (v) values ('local-tagged')"
log=$(q $rep 34307 "show binary log status" 2>/dev/null | awk '{print $1}' || true)
[ -n "$log" ] || log=$(q $rep 34307 "show master status" | awk '{print $1}')
docker stop -t 60 $rep >/dev/null
docker cp $rep:/var/lib/mysql/$log $out
docker start $rep >/dev/null
for i in $(seq 120); do q $rep 34307 "select 1" >/dev/null && break; sleep 1; done
docker exec $rep sh -c "mysqlbinlog -vv /var/lib/mysql/$log" 
docker rm -f $src $rep >/dev/null
