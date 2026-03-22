# K8SPS-642: Point-in-Time Recovery

| Field        | Value                                    |
|--------------|------------------------------------------|
| Author       | Ege                                      |
| Status       | Draft                                    |
| Created      | 2026-03-20                               |
| Last Updated | 2026-03-20                               |
| Reviewers    |                                          |

---

## 1. Overview [REQUIRED]

Point-in-Time Recovery (PITR) allows users to restore a MySQL cluster to any specific moment in time, recovering from accidental data loss (e.g., a dropped table) by replaying binary logs on top of a base backup. The operator collects binary logs via Percona Binlog Server (PBS) that streams them to S3, and replays them during restore using MySQL's replication applier — converting binlogs to relay logs and replaying them via the SQL thread rather than piping through `mysqlbinlog`. This approach enables faster recovery through MySQL's parallel replication infrastructure.

### 1.1 Goals

- Continuously capture binary logs from the MySQL cluster via a binlog server and store them in S3
- Enable users to restore to a specific point in time using either a GTID set or a datetime timestamp
- Use the relay-log-replay technique (rather than `mysqlbinlog` piping) for faster, parallelizable binlog application
- Integrate PITR into the existing restore reconciler as an additional phase after base backup restore

### 1.2 Non-Goals (Out of Scope)

- GCS and Azure support for binlog storage: Only S3 is supported for the binlog server. May be revisited when `binlog_server` binary adds support for other backends.
- Parallel relay log replay configuration: The implementation uses MySQL defaults for `replica_parallel_workers`. Explicit tuning may be revisited based on user feedback.
- Binlog retention policy: Binlogs accumulate in S3 with no automatic cleanup.

---

## 2. Background [REQUIRED]

### 2.1 Core Concepts

**Binary log server (`binlog_server`):** A Percona tool that acts as a replication client, connecting to MySQL and pulling binary logs in real time. It stores them in S3 as individual files, maintaining metadata (GTIDs, timestamps) for each binlog. The operator deploys it as a single-replica StatefulSet.

**Relay-log-replay technique for PITR:** Traditional PITR pipes binary logs through `mysqlbinlog`, which is single-threaded and slow. Instead, this implementation downloads binlogs from S3, renames them as relay logs, and replays them using MySQL's replication SQL thread via `CHANGE REPLICATION SOURCE TO RELAY_LOG_FILE=..., SOURCE_HOST='dummy'` followed by `START REPLICA SQL_THREAD UNTIL ...`. This technique is described in detail by [lefred's blog post on faster MySQL PITR](https://lefred.be/content/howto-make-mysql-point-in-time-recovery-faster/). The key advantage is that MySQL's SQL thread can leverage parallel applier workers for concurrent transaction replay.

**GTID-based vs date-based recovery targets:**
- *GTID-based:* User specifies a GTID set (e.g., `uuid:N`). The SQL thread replays until just before that GTID using `START REPLICA SQL_THREAD UNTIL SQL_BEFORE_GTIDS`.
- *Date-based:* User specifies a datetime. The last relay log is parsed with `mysqlbinlog --stop-datetime` to find the exact binary log position, then `START REPLICA SQL_THREAD UNTIL RELAY_LOG_FILE, RELAY_LOG_POS` is used.

**Binlog search:** The `binlog_server` binary provides `search_by_gtid_set` and `search_by_timestamp` subcommands that query S3 metadata to return the list of binlog files covering the range from the backup's GTID position to the target recovery point. The operator executes these via `kubectl exec` on the binlog server pod.

### 2.2 Key Constraints

1. **S3 only for binlog storage:** The `binlog_server` binary currently supports S3 as its storage backend. The binlog server configuration uses an S3 URI with embedded credentials.

2. **GTID mode required:** The relay-log-replay technique requires `gtid_mode=ON`. The operator enforces this for all clusters. The PITR entrypoint script starts mysqld with `--gtid-mode=ON --enforce-gtid-consistency=ON`.

3. **Single binlog server replica:** The binlog server connects as a replication client with a specific `server_id`. Only one instance can connect with the same ID, so the StatefulSet is hardcoded to 1 replica regardless of the `size` field in the CR.

4. **Cluster must be paused during PITR:** The restore reconciler pauses the entire cluster before running the base restore and PITR jobs. The PITR job starts its own temporary mysqld instance on the data PVC.

5. **Binlog server must be running before restore:** The restore reconciler searches binlogs by executing commands on the binlog server pod. The search must happen before the cluster is paused (since pausing would terminate the binlog server pod).

---

## 3. Architecture [REQUIRED]

### 3.1 Architecture Before This Change

```
Backup CR → Backup Job (XtraBackup) → S3/GCS/Azure

Restore CR → Restore Reconciler
  → Pause cluster
  → XtraBackup restore job
  → Unpause cluster
```

### 3.2 Architecture After This Change

```
Continuous binlog collection:
  MySQL Primary
    ← Binlog Server (replication client, GTID mode)
      → S3 (binlog files with GTID/timestamp metadata)

Restore with PITR:
  Restore CR (with pitr spec)
    → Restore Reconciler
      1. Search binlogs (exec on binlog server pod, BEFORE pause)
         → Creates ConfigMap with binlog entry list (JSON)
      2. Pause cluster (scale all components to 0)
      3. Run XtraBackup restore job (restore base backup to PVC)
      4. Run PITR job (on mysql-0 PVC):
         a. Init container copies pitr binary and entrypoint
         b. Entrypoint starts temporary mysqld with:
            --admin-address=127.0.0.1
            --skip-replica-start
            --read-only=ON --super-read-only=ON
            --gtid-mode=ON --enforce-gtid-consistency=ON
         c. Setup phase: download binlogs from S3, write as relay logs + index
         d. Apply phase:
            - CHANGE REPLICATION SOURCE TO RELAY_LOG_FILE=..., SOURCE_HOST='dummy'
            - START REPLICA SQL_THREAD UNTIL <GTID or position>
            - Wait for SQL thread to stop
            - STOP REPLICA; RESET REPLICA ALL
         e. Shutdown mysqld
      5. Unpause cluster
```

---

## 4. CRD and Interface Changes [REQUIRED]

### 4.1 CRD Spec Changes

- **`spec.pitr`** *(optional)* on `PerconaServerMySQLRestore`:
  Enables point-in-time recovery on a restore operation. When present, the restore reconciler runs a PITR phase after the base backup restore.

- **`spec.pitr.type`** *(required, one of `"gtid"`, `"date"`)*:
  Specifies the recovery target type. Determines which search function is called on the binlog server and which `START REPLICA UNTIL` variant is used.

- **`spec.pitr.gtid`** *(required when type is `"gtid"`)*:
  The GTID set to recover to. Replay stops just *before* this GTID (using `SQL_BEFORE_GTIDS`), so the transaction identified by this GTID is NOT applied. This is the "exclude" semantic — the user specifies the bad transaction they want to skip.

- **`spec.pitr.date`** *(required when type is `"date"`)*:
  ISO datetime string (e.g., `"2026-03-20 10:30:00"`). Replay stops at the last event before this timestamp. The last relay log is parsed with `mysqlbinlog --stop-datetime` to find the exact position.

```go
type RestorePITRSpec struct {
    Type PITRType `json:"type"`
    Date string   `json:"date,omitempty"`
    GTID string   `json:"gtid,omitempty"`
}

type PITRType string

const (
    PITRGtid PITRType = "gtid"
    PITRDate PITRType = "date"
)
```

> [!note]
> Actually I would prefer different field names and values. Like:
> ```
> type: datetime
> datetime: "2026-03-22 16:08:00"
> ```
> But I decided to keep consistency with K8SPXC.

### 4.2 CRD Status Changes

No new status fields. The existing `RestoreState` values (`Starting`, `Running`, `Succeeded`, `Failed`) are reused. The `Running` state covers both the base restore job and the PITR job — the restore transitions to `Succeeded` only after the PITR job completes.

### 4.3 Internal Contracts

**Binlog server configuration (Secret):** The config JSON was expanded with fields required by the `binlog_server` binary:

| Field | Purpose |
|-------|---------|
| `connection.ssl` | SSL settings (mode, CA, cert, key paths) for connecting to MySQL |
| `replication.mode` | Set to `"gtid"` to enable GTID-based replication |
| `replication.verify_checksum` | Enables binlog event checksum verification |
| `replication.rewrite` | Controls binary log file naming and size (`base_file_name: "binlog"`, `file_size: "128M"`) |
| `storage.backend` | Set to `"s3"` |
| `storage.fs_buffer_directory` | Local buffer directory for in-flight binlog data (`/var/lib/binlogsrv`) |
| `storage.checkpoint_size` / `checkpoint_interval` | Controls how frequently data is flushed to S3 |

**S3 URI format change:** The S3 URI was changed from `s3://accessKey:secretKey@bucket.region` to `protocol://accessKey:secretKey@host/bucket` to correctly support custom endpoints (e.g., MinIO). The protocol is extracted from the `endpointURL` field.

**PITR binlogs ConfigMap:** Created by the restore reconciler before pausing the cluster. Contains a JSON array of `BinlogEntry` objects from the binlog search response:

```json
[
  {
    "name": "binlog.000001",
    "size": 1234,
    "uri": "https://minio:9000/bucket/binlog.000001",
    "previous_gtids": "uuid:1-10",
    "added_gtids": "uuid:11-20",
    "min_timestamp": "2026-03-20T08:00:00Z",
    "max_timestamp": "2026-03-20T09:00:00Z"
  }
]
```

**PITR job environment variables:**

| Variable | Source | Purpose |
|----------|--------|---------|
| `RESTORE_NAME` | Restore CR name | Job identification |
| `BINLOGS_PATH` | Hardcoded path to ConfigMap mount | Location of binlog entry JSON |
| `PITR_TYPE` | `spec.pitr.type` | `"gtid"` or `"date"` |
| `PITR_GTID` | `spec.pitr.gtid` | Target GTID (when type is gtid) |
| `PITR_DATE` | `spec.pitr.date` | Target datetime (when type is date) |
| `AWS_ACCESS_KEY_ID` | S3 credentials secret | S3 authentication |
| `AWS_SECRET_ACCESS_KEY` | S3 credentials secret | S3 authentication |
| `AWS_DEFAULT_REGION` | `s3.region` | S3 region |
| `AWS_ENDPOINT` | `s3.endpointURL` | S3 endpoint (for MinIO etc.) |
| `S3_BUCKET` | `s3.bucket` | S3 bucket name |

**New binaries installed by init container:**
- `/opt/percona/pitr` — Go binary with `setup` and `apply` subcommands
- `/opt/percona/run-pitr-restore.sh` — Shell entrypoint that starts mysqld, runs pitr setup/apply, then shuts down mysqld

### 4.4 User-Facing Behavior Changes

- When `spec.pause` is set to `true` on a cluster, the main reconciler now also disables PITR (`spec.backup.pitr.enabled = false`) to prevent the binlog server from running while the cluster is paused.
- The binlog server StatefulSet is now cleaned up (deleted) when PITR is disabled, via a new `cleanupBinlogServer` function in the main reconciler.
- The binlog server replicas are hardcoded to 1 regardless of any size configuration.

---

## 5. Design Decisions and Alternatives [REQUIRED]

### 5.1 Relay-Log Replay vs `mysqlbinlog` Piping

**Chosen approach:** Download binlogs from S3, rename as relay logs, replay via MySQL's replication SQL thread.

**Why:** The `mysqlbinlog` approach is single-threaded and cannot leverage MySQL's parallel applier infrastructure. The relay-log approach uses the same mechanism as normal replication, which MySQL already optimizes with `LOGICAL_CLOCK` parallel workers. Even without explicit parallel configuration, this approach is at least as fast as `mysqlbinlog` and has a path to parallelism.

**Alternatives considered:**

| Alternative | Why Rejected |
|------------|--------------|
| Pipe binlogs through `mysqlbinlog \| mysql` | Single-threaded, no path to parallelism. |

### 5.2 Two-Phase PITR Job (setup + apply) as Subcommands

**Chosen approach:** Single Go binary (`pitr`) with `setup` and `apply` subcommands, orchestrated by a shell entrypoint (`run-pitr-restore.sh`).

**Why:** The setup phase (downloading binlogs from S3) does not need mysqld running. The apply phase requires a running mysqld. The shell entrypoint handles the mysqld lifecycle (start, wait for ready, run setup, run apply, shutdown). Keeping setup and apply as separate subcommands makes each phase independently testable and debuggable.

**Alternatives considered:**

| Alternative | Why Rejected |
|------------|--------------|
| Single binary that manages mysqld lifecycle | Adds complexity — managing mysqld process lifecycle in Go requires signal handling, health checking, and graceful shutdown. The shell entrypoint handles this naturally with `mysqld &`, `mysqladmin ping`, and `mysqladmin shutdown`. |
| Two separate Kubernetes jobs (one for setup, one for apply) | Would require the reconciler to manage job sequencing and an intermediate state between setup and apply. Adds reconciler complexity for no user benefit. |

### 5.3 Binlog Server Hardcoded to 1 Replica

**Chosen approach:** Override the StatefulSet replicas to `ptr.To(int32(1))`, ignoring any `size` configured in the CR.

**Why:** Due to Constraint 3 — only one binlog server can connect to MySQL with a given `server_id`. Running multiple replicas would cause replication conflicts. The `size` field in the CR is ignored to prevent user misconfiguration.

### 5.4 Disable PITR When Cluster is Paused

**Chosen approach:** Set `spec.backup.pitr.enabled = false` when `spec.pause = true` in `CheckNSetDefaults`.

**Why:** A paused cluster has no MySQL pods running. The binlog server would fail to connect and enter a crash loop. Disabling PITR cleanly prevents this.

---

## 6. Replication Model Impact [OPTIONAL]

PITR is replication-model-agnostic in its current implementation. Both Group Replication and Async clusters produce binary logs, and the binlog server connects as a standard replication client using GTID mode. The PITR job replays on a single pod's PVC regardless of topology.

---

## 7. User Experience [REQUIRED]

### 7.1 Existing CR (Unchanged)

```yaml
# Existing restores without PITR continue to work identically.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore1
spec:
  clusterName: cluster1
  backupName: backup1
```

### 7.2 Enable PITR Binlog Collection

```yaml
# In PerconaServerMySQL CR, enable binlog collection:
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: cluster1
spec:
  backup:
    pitr:
      enabled: true
      binlogServer:
        storage:
          s3:
            bucket: my-binlogs
            credentialsSecret: my-s3-secret
            region: us-east-1
            endpointURL: https://s3.amazonaws.com
```

### 7.3 Restore with PITR (GTID-based)

```yaml
# Recover to just before a specific bad transaction.
# The transaction identified by the GTID is NOT applied.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore-pitr
spec:
  clusterName: cluster1
  backupName: backup1
  pitr:
    type: gtid
    gtid: "cc5e06e7-241e-11f1-a165-522d36bd0c5e:2254"
```

### 7.4 Restore with PITR (Date-based)

```yaml
# Recover to a specific point in time.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore-pitr-date
spec:
  clusterName: cluster1
  backupName: daily-backup
  pitr:
    type: date
    date: "2026-03-20 09:15:00"
```

---

## 8. Error Handling and Edge Cases [REQUIRED]

### 8.1 No Binlogs Found for Recovery Target

**Scenario:** The user specifies a PITR target (GTID or date) for which no matching binlogs exist in S3. This can happen if the binlog server was not running during the time period, or if the target is before the backup.

**Expected behavior:**
- `searchBinlogs` returns an empty slice.
- `reconcilePITRConfig` returns an error: `"no binlogs found for the given PITR target"`.
- The reconciler returns the error; the restore status transitions to `Failed`.
- The user sees the error in the restore controller logs and restore status.

### 8.2 Binlog Server Pod Not Ready

**Scenario:** The binlog server pod is not running or not ready when the restore reconciler tries to search binlogs.

**Expected behavior:**
- `getBinlogServerPod` checks pod readiness via `k8s.IsPodReady()`.
- Returns error: `"binlog server pod <name> is not ready"`.
- The reconciler requeues and retries.

### 8.3 Replication Worker Error During Replay

**Scenario:** A relay log event fails to apply (e.g., data inconsistency between backup and binlog).

**Expected behavior:**
- The SQL thread stops with an error.
- `WaitReplicaSQLThreadStop` detects `SERVICE_STATE = OFF` and queries `replication_applier_status_by_worker` for all workers' error status.
- Returns error with error number and message: `"replication worker error N: <message>"`.
- The PITR job fails; restore status transitions to `Failed`.

### 8.4 Date-Based Recovery — No Matching Position in Last Relay Log

**Scenario:** `mysqlbinlog --stop-datetime` finds no events matching the given date in the last relay log (e.g., the date is beyond the last binlog).

**Expected behavior:**
- The regex `end_log_pos (\d+)` finds no matches.
- `getStopPosition` returns error: `"no end_log_pos found in mysqlbinlog output"`.
- The PITR job fails with a clear error message.

### 8.5 S3 Download Failure During Setup

**Scenario:** Network error or credentials issue when downloading binlogs from S3 during the setup phase.

**Expected behavior:**
- `s3Client.GetObject` returns an error.
- The setup phase fails: `"download binlog <uri>: <error>"`.
- The PITR job fails; restore status transitions to `Failed`.

### 8.6 mysqld Fails to Start in PITR Job

**Scenario:** The temporary mysqld started by the entrypoint script fails (e.g., corrupted data directory from a failed restore).

**Expected behavior:**
- The `mysqladmin ping` loop in the entrypoint never succeeds.
- The job eventually times out.
- Restore status transitions to `Failed`.

---

## 9. Migration and Backward Compatibility [REQUIRED]

### 9.1 Existing Clusters

- Existing clusters with PITR enabled (`spec.backup.pitr.enabled: true`) will get an updated binlog server configuration (SSL, replication mode, storage backend fields). The binlog server StatefulSet will be updated with the new config hash, triggering a rolling restart. This is safe — the binlog server reconnects and resumes from its checkpoint.
- Existing Restore CRs without `spec.pitr` continue to work identically — the PITR phase is skipped when the field is absent.

### 9.2 CRD Compatibility

- All changes are additive:
  - New `RestorePITRSpec` type with `spec.pitr` field on PerconaServerMySQLRestore
  - New `PITRType` constants
- `make generate` and `make manifests` produce updated CRD YAML with the new fields.
- No breaking changes to existing CRDs.

### 9.3 Operator Version Skew

- **Operator upgraded, CR unchanged:** The binlog server gets the expanded configuration on next reconciliation. If the binlog server binary doesn't support the new config fields, it may fail.
- **Restore CR with `pitr` field on old operator:** If CRDs are updated, the old operator ignores the unknown `pitr` field and performs a regular restore without PITR. No error, but the user doesn't get PITR. If CRDs are not updated, `kubectl apply` fails wiith `unknown field: spec.pitr`.

---

## 10. Testing Strategy [REQUIRED]

### 10.1 E2E Test Scenarios

| Scenario | Cluster Type | What It Validates |
|----------|-------------|-------------------|
| Enable PITR, take backup, insert data, restore with GTID target | GR + Async | Full PITR flow: binlog collection, search, download, relay-log replay, data consistency at target GTID |
| Enable PITR, take backup, insert data, restore with date target | GR + Async | Date-based recovery: mysqlbinlog position parsing, correct stop point |
| Restore without PITR (backward compatibility) | GR + Async | Existing restore flow unchanged when `spec.pitr` is absent |
| PITR with invalid GTID (no matching binlogs) | GR + Async | Error handling: restore reports meaningful error, doesn't hang |
| PITR with binlog server not running | GR + Async | Error handling: restore reports binlog server pod not ready |
| Disable PITR and verify binlog server cleanup | GR + Async | cleanupBinlogServer deletes StatefulSet and config Secret |
| Cluster pause with PITR enabled | GR + Async | PITR is disabled when cluster is paused, binlog server is cleaned up |

---

## 11. Open Questions [REQUIRED]

1. **Binlog server connects to pod-0 only:** The binlog server always connects to `mysql-0` via FQDN. In async replication, if the primary fails over to another pod, the binlog server may connect to a replica and miss transactions (or fail if `log_replica_updates` is off). Should the binlog server connect to the primary service instead?
   - *Option A:* Connect to pod-0 (current). Simple, but may miss transactions after failover.
   - *Option B:* Connect to the primary service endpoint. Handles failover, but the binlog server may need to re-negotiate replication position after failover.
   - *Recommendation:* Option B for production readiness, but requires testing with the binlog_server binary's reconnection behavior.
   - *Resolution:* Pending.

2. **Replicating from primary:** Even if we resolve the question above we also need to decide if PBS will replicate from a secondary or the primary.
   - *Option A:* Use a headless service that matches all MySQL pods. It's okay if the primary is used to replica from.
   - *Option B:* Prefer a secondary to minimize the load on the primary.
   - *Recommendation:* Option B might be better depending on implementation complexity.
   - *Resolution:* Pending.

3. **Binlog retention in S3:** Binlogs accumulate indefinitely. Should the operator implement a retention policy?
   - *Recommendation:* Add retention support in a follow-up. Users can manually clean up S3 for now.
   - *Resolution:* Deferred.

4. **PITR for non-S3 storage:** Should the binlog server support GCS and Azure?
   - *Recommendation:* Depends on upstream `binlog_server` support. Not a blocker for initial release.
   - *Resolution:* Deferred.

5. **`SQL_BEFORE_GTIDS` semantics clarity:** The GTID target uses `SQL_BEFORE_GTIDS`, meaning the specified transaction is *excluded*. Should the CRD documentation make this explicit, or should we offer both "before" and "at" semantics?
   - *Recommendation:* Document the exclusion semantic clearly. This is consistent with the behavior in K8SPXC.
   - *Resolution:* Pending.

6. **Parallel replay workers:** Should the operator let users to configure `replica_parallel_workers`?
   - *Option A:* Use MySQL defaults (4 workers in 8.4).
   - *Option B:* Add a `parallelWorkers` field to `RestorePITRSpec`.
   - *Recommendation:* Start with Option A, add Option B based on user feedback about PITR speed.
   - *Resolution:* Pending.

7. **Downloading binary logs:** Operator downloads all binary logs collected by PBS without considering if they are needed or not. Should we implement a smarter logic to decide which slice of binary logs needs to be downloaded?
    - *Option A:* Relay log based recovery already handles transactions that are already applied to database. No need to overcomplicate.
    - *Option B*: If PBS is collecting binary logs for a long time, `pitr` binary might need to download a lot of files and this might delay the recovery. Operator needs to be smarter to understand last write and/or last GTID of the backup and decide from where it needs to start downloading files.
   - *Recommendation:* Check the possibility of implementing Option B. If possible, we should do it.
   - *Resolution:* Pending.

---

## Appendix

### A. Glossary

| Term | Definition |
|------|------------|
| GR | Group Replication — MySQL's multi-primary replication protocol |
| Async | Asynchronous replication — traditional primary-replica replication |
| PITR | Point-in-Time Recovery via binary log replay |
| Binlog server | Percona tool that replicates binary logs from MySQL to S3 |
| Relay log | MySQL's local copy of binary logs received from a replication source; the SQL thread reads from relay logs |
| GTID | Global Transaction Identifier — unique ID for each committed transaction |
| `SQL_BEFORE_GTIDS` | MySQL clause to stop replication replay just before the specified GTID |

### B. References

- [HowTo: Make MySQL Point-in-Time Recovery Faster (lefred)](https://lefred.be/content/howto-make-mysql-point-in-time-recovery-faster/)
- [MySQL Point in Time Recovery the Right Way](https://www.percona.com/blog/mysql-point-in-time-recovery-right-way/)
- [MySQL 8.0 Reference: Point-in-Time Recovery Using Binary Log](https://dev.mysql.com/doc/refman/8.0/en/point-in-time-recovery.html)
- [MySQL 8.0 Reference: CHANGE REPLICATION SOURCE TO](https://dev.mysql.com/doc/refman/8.0/en/change-replication-source-to.html)
- [MySQL 8.0 Reference: START REPLICA](https://dev.mysql.com/doc/refman/8.0/en/start-replica.html)
