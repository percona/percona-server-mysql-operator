# This is test change
# K8SPS-410: Incremental Backup Support

| Field        | Value              |
|--------------|--------------------|
| Author       | @mayankshah1607    |
| Status       | Completed          |
| Created      | 2026-03-18         |
| Last Updated | 2026-04-02         |
| Reviewers    | @egegunes @gkech @hors            |
| Implementation    | https://github.com/percona/percona-server-mysql-operator/pull/1269            |

---

## 1. Overview

This feature adds incremental backup support to the Percona Server MySQL Operator. Currently the operator only supports full backups via Percona XtraBackup (PXB). Incremental backups capture only data pages changed since a previous backup, significantly reducing backup time and storage usage.

### 1.1 Goals

- Support on-demand and scheduled incremental backups
- Maintain backup chains (full → inc1 → inc2 → ... → incN)
- Support restore from any point in an incremental chain
- Preserve full backward compatibility with existing full-only backup workflows
- Support all existing storage backends (S3, GCS, Azure)

### 1.2 Non-Goals (Out of Scope)

- Page tracking acceleration (`component_mysqlbackup`) — can be added later as an optimization without API changes (see references)
- Merging/compacting incremental chains into a single full backup — complexity not justified for initial release
- Incremental backups combined with PiTR (binlog-based point-in-time recovery) — requires separate work

---

## 2. Background

### 2.1 Core Concepts

**Log Sequence Numbers (LSN).** Every InnoDB page carries an LSN indicating when it was last modified. An incremental backup copies only pages with an LSN newer than a reference LSN. The result is a set of `.delta` files rather than full tablespace copies.

**xtrabackup_checkpoints.** Each PXB backup produces this file containing:

```
backup_type = full-backuped | incremental
from_lsn = <starting LSN>
to_lsn = <checkpoint LSN at backup end>
last_lsn = <final copied LSN>
```

The chain must be unbroken: each incremental's `from_lsn` must equal the prior backup's `to_lsn`.

**Incremental via explicit LSN.** When backups are streamed to cloud storage (as the operator does), no local copy of the prior backup exists. PXB supports `--incremental-lsn=<LSN>` to specify the starting LSN directly, removing the need for a local `--incremental-basedir`.

**Incremental restore (prepare).** Restoring from a chain is a multi-step process:

1. Prepare the base (full) backup with `--apply-log-only` (redo only, no rollback)
2. Apply each incremental in order, all with `--apply-log-only` except the last
3. Apply the final incremental without `--apply-log-only` (allows rollback)
4. Move back to datadir as with any full backup

Critical constraints of the prepare process:
- `--apply-log-only` is mandatory for all steps except the final one; omitting it makes subsequent incrementals unusable
- Each `--incremental-dir` is modified during prepare and cannot be reused
- An interrupted prepare corrupts the backup

**Streaming.** The operator streams backups via `xtrabackup --backup --stream=xbstream | xbcloud put`. Incremental streaming works identically — `.delta` files are streamed instead of full `.ibd` files. Each backup is stored as an independent object in cloud storage.

### 2.2 Key Constraints

1. **No local basedir available.** Backups are streamed directly to cloud storage. The sidecar has no local copy of previous backups. The operator must use `--incremental-lsn=<LSN>` rather than `--incremental-basedir`.

2. **LSN must be retrievable.** The `to_lsn` from `xtrabackup_checkpoints` must be retrievable after each backup so subsequent incremental backups can reference it.

3. **Restore requires all chain members.** The restore process must download the full base backup plus all incremental backups in the chain and apply them in order.

4. **Chain integrity is critical.** If any backup in the chain is deleted or corrupted, all subsequent incrementals become unrestorable.

5. **Backup destination paths.** Each backup gets a unique destination path. Incremental backups need separate paths but must be logically linked.

6. **Chain must be discoverable across clusters.** For cross-cluster restore (`backupSource`), the target cluster has no access to the source cluster's backup CRs. The chain structure must be discoverable from the storage alone.

---

## 3. Architecture

### 3.1 Architecture Before This Change

**Backup flow:**
```
User creates PerconaServerMySQLBackup CR
  → Backup Controller reconciles
    → Validates cluster, storage, resolves source pod
    → Creates K8s Job (run-backup.sh)
      → Job POSTs BackupConfig JSON to sidecar HTTP API (port 6450)
        → Sidecar runs: xtrabackup --backup --stream=xbstream | xbcloud put
    → Controller monitors Job, updates CR status
```

**Restore flow:**
```
User creates PerconaServerMySQLRestore CR
  → Restore Controller reconciles
    → Pauses cluster (Spec.Pause=true, waits for pods=0)
    → Creates K8s Job (run-restore.sh)
      → Job runs: xbcloud get | xbstream -x → xtrabackup --prepare → --move-back
    → Unpauses cluster
```

### 3.2 Architecture After This Change

**Backup flow (incremental):**
```
User creates PerconaServerMySQLBackup CR with type: incremental
  → Backup Controller reconciles
    → setIncrementalBaseAnnotations():
      → Resolves base backup (explicit via spec.incrementalBaseBackupName,
        or auto-resolves latest succeeded full backup for same clusterName + storageName)
      → Validates base backup is succeeded and uses same storage
      → Sets percona.com/base-backup-name annotation with base backup's destination name
    → getLastBackupLSN():
      → Finds latest succeeded backup (full or incremental) for same cluster
      → Calls sidecar's /backup/checkpoint-info endpoint
        → Sidecar fetches xtrabackup_checkpoints from storage via xbcloud get
        → Parses and returns to_lsn
    → Computes destination: <base-name>.incr/<cluster>-<timestamp>-incr
    → Creates K8s Job (run-backup.sh)
      → Sets INCREMENTAL_LSN env var on the job container
      → Job POSTs BackupConfig (includes incrementalLsn) to sidecar HTTP API
        → Sidecar runs: xtrabackup --backup --stream=xbstream
            --incremental-lsn=<LSN> | xbcloud put <computed-destination>
    → Updates CR status with type and destination
```

**Restore flow (unified for both backupName and backupSource):**
```
User creates PerconaServerMySQLRestore CR
  → Restore Controller reconciles
    → Resolves backup destination:
      - backupName: looks up backup CR → gets status.destination + storage config
      - backupSource: reads destination + storage config from spec
    → Checks if backup type is incremental (via CR type or destination path)
    → For incremental restore:
      → resolveIncrementalChain():
        → Parses destination to extract base path (IncrementalBaseDestination())
          and incrementals dir (IncrementalsDir())
        → Connects to storage, lists all objects under ".incr/" prefix
        → Extracts timestamp-based names, sorts lexicographically
        → Filters to include only entries ≤ target backup timestamp
        → Returns DestinationInfo{Base, Incrementals}
      → Pauses cluster
      → Creates K8s Job with env vars:
        - BACKUP_DEST = base backup path
        - BACKUP_INCREMENTALS_DEST = comma-separated incremental paths
        → Job downloads and extracts base backup
        → Prepares base with --apply-log-only
        → For each incremental: download, extract, apply with --apply-log-only
          (except the last one, which omits --apply-log-only)
        → Move back to datadir
      → Unpauses cluster
    → For full restore: existing logic unchanged
```

### 3.3 Key Observations

1. **Chain structure is implicit in the storage layout.** Incremental backups for a base backup `cluster-ts-full` are stored under `cluster-ts-full.incr/<cluster>-<ts>-incr`. The chain is discoverable from the path convention alone — no separate metadata files needed. Any cluster with access to the storage bucket can reconstruct the chain by listing the `.incr/` prefix.

2. **Sidecar remains stateless.** The sidecar has no persistent storage and no knowledge of previous backups. The LSN is passed explicitly via BackupConfig (`incrementalLsn` field). The sidecar also exposes a `/backup/checkpoint-info` endpoint that fetches `xtrabackup_checkpoints` from storage on demand and returns the parsed checkpoint info. No metadata file upload step.

3. **Controller is the chain coordinator.** The backup controller finds the latest succeeded backup, calls the sidecar's checkpoint endpoint to retrieve its `to_lsn` from storage, and passes it to the backup job via the `INCREMENTAL_LSN` environment variable. It also computes the storage destination using the `.incr/<cluster>-<timestamp>-incr` convention and sets chain-tracking annotations.

4. **Restore flow is unified.** Both `backupName` (in-place) and `backupSource` (cross-cluster) restores follow the same path: resolve destination → parse path to detect incremental → list `.incr/` prefix to discover chain members → restore. No additional metadata is needed.

5. **Restore Job needs multi-backup awareness.** Today the restore Job handles a single backup. Incremental restore requires downloading and applying multiple backups in sequence, with careful ordering and prepare flags.

6. **Existing sidecar concurrency guard.** The sidecar already prevents concurrent backups via an atomic bool check. This naturally prevents two incremental backups from racing on the same chain.

---

## 4. CRD and Interface Changes

### 4.1 CRD Spec Changes

- **`PerconaServerMySQLBackup.spec.type`** *(optional, enum: `[full, incremental]`, default: `"full"`)*:
  Specifies whether this is a `full` or `incremental` backup. When set to `incremental`, the operator automatically resolves the most recent succeeded full backup for the same `clusterName` + `storageName` as the chain base (unless `incrementalBaseBackupName` is specified). Validation rejects `incremental` if no succeeded full backup exists for the matching cluster and storage.

- **`PerconaServerMySQLBackup.spec.incrementalBaseBackupName`** *(optional, string pointer)*:
  Name of a specific full backup to use as the base for this incremental backup. Only valid when `type` is `incremental`. When omitted, the operator auto-resolves the latest succeeded full backup.

- **`BackupSchedule.type`**: Specifies the type of backup (`[full, incremental]`) for scheduled backups.

- **No new restore spec fields.** When `backupName` references an incremental backup, the operator automatically resolves and restores the full chain (full → inc1 → ... → incN). This is the only valid behavior for incremental backups — an incremental backup cannot be restored in isolation. When `backupName` references a full backup, the existing restore logic runs unchanged.

### 4.2 CRD Status Changes

New field on `PerconaServerMySQLBackupStatus`:

- **`type`**: Records whether this was a `full` or `incremental` backup (mirrored from spec after backup completes).

New kubebuilder print column on `PerconaServerMySQLBackup`:

- `Type` (from `status.type`)

### 4.3 Internal Contracts

**BackupConfig (operator → sidecar).** One new field added to the JSON payload POSTed to the sidecar's `/backup/{backupName}` endpoint:

- `incrementalLsn` *(string, optional)*: The LSN to pass as `--incremental-lsn` to xtrabackup. Only set for incremental backups.

Old sidecars ignore this field. New sidecars treat an empty `incrementalLsn` as a full backup. Note: no `backupType` field is sent in BackupConfig — the presence of `incrementalLsn` is sufficient to trigger incremental behavior.

**LSN retrieval (controller → sidecar → storage).** The sidecar exposes a `/backup/checkpoint-info` endpoint. When the controller needs the `to_lsn` for creating the next incremental backup, it:

1. Finds the last succeeded backup (full or incremental) for the cluster
2. POSTs that backup's `BackupConfig` (with storage credentials and destination) to the sidecar's `/backup/checkpoint-info` endpoint
3. The sidecar runs `xbcloud get` to download `xtrabackup_checkpoints` from that backup's storage location, pipes through `xbstream` extraction, and parses the checkpoint file
4. Returns a `CheckpointInfo` struct containing `backup_type`, `from_lsn`, `to_lsn`, `last_lsn`, `flushed_lsn`, and `redo_memory`/`redo_frames`
5. The controller extracts `to_lsn` and passes it as `INCREMENTAL_LSN` env var to the backup job

**Storage directory convention.** The chain structure is encoded in the storage paths:

```
<storage-prefix>/
  cluster-2026-03-15-full/                                ← base (full) backup data (unchanged)
  cluster-2026-03-15-full.incr/                           ← incremental backups directory for given base
    cluster-2026-03-16T000000-incr/                       ← first incremental
    cluster-2026-03-17T000000-incr/                       ← second incremental
    cluster-2026-03-18T000000-incr/                       ← third incremental
```

Destination naming format:
- Full backups: `<cluster>-<timestamp>-full`
- Incremental backups: `<base-backup-name>.incr/<cluster>-<timestamp>-incr`

Key properties:
- The `.incr/` suffix deterministically links incrementals to their base backup.
- Each incremental includes the cluster name and creation timestamp with an `-incr` suffix, ensuring natural sort order = chain order.
- Given any incremental's destination (e.g., `prefix/cluster-2026-03-15-full.incr/cluster-2026-03-17T000000-incr`), the base backup path and the full chain up to that point can be computed without any metadata files.

**Restore Job environment variables.** The restore Job uses `BACKUP_DEST` for the base backup path (same as existing full restores). One new env var is added for incremental restores:

- `BACKUP_INCREMENTALS_DEST`: Comma-separated list of incremental backup destinations, in chain order.

When `BACKUP_INCREMENTALS_DEST` is set (non-empty), the restore script runs the incremental restore flow. When unset, the existing full restore logic runs unchanged.


### 4.4 Chain Annotations and Labels on Backup CRs

**Annotation for chain linkage:**

- `percona.com/base-backup-name`: Stores the name (as in the storage) of the base (full) backup that an incremental backup belongs to. This is used to determine chain membership when resolving LSN for the next incremental backup.

### 4.5 User-Facing Behavior Changes

`kubectl get ps-backups` output gains a `TYPE` column:

```
$ kubectl get ps-backups
NAME              STORAGE   TYPE          STATE       COMPLETED
weekly-full-1     s3-us     full          Succeeded   2026-03-15T00:00:00Z
daily-inc-mon     s3-us     incremental   Succeeded   2026-03-16T00:00:00Z
daily-inc-tue     s3-us     incremental   Succeeded   2026-03-17T00:00:00Z
daily-inc-wed     s3-us     incremental   Running     -
```

---

## 5. Design Decisions and Alternatives

### 5.1 LSN Passing: Explicit LSN vs. Local Basedir

**Chosen approach:** Pass `--incremental-lsn=<LSN>` explicitly to xtrabackup.

**Why:** Due to Constraint 1 (no local basedir available), the sidecar has no local copy of previous backups. The LSN is retrieved from storage via the sidecar's checkpoint endpoint and passed to the backup job via BackupConfig.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Download previous backup's `xtrabackup_checkpoints` to a local dir and use `--incremental-basedir` | Adds unnecessary storage I/O and complexity; the LSN is a single integer that can be passed directly |

### 5.2 Chain Discovery: Directory Convention vs. Metadata Files

**Chosen approach:** The chain structure is encoded in the storage directory layout. Incremental backups for a base backup at `<dest>` are stored under `<dest>.incr/<cluster>-<timestamp>-incr`. The chain is discoverable by parsing the destination path and listing the `.incr/` prefix — no separate metadata files needed. The `toLsn` needed for creating incremental backups is fetched on demand from storage via the sidecar's checkpoint endpoint.

**Why:** The directory convention is the simplest possible approach:
- **No extra artifacts:** No metadata JSON files to write, read, or keep in sync. The chain is the directory structure itself.
- **Cross-cluster restore:** When restoring via `backupSource`, the target cluster has no access to source CRs. The directory convention makes the chain discoverable from the destination path alone — parse the path, list the prefix, sort by timestamp.
- **Deterministic:** Given any incremental destination, the base path and all prior incrementals can be computed without any lookups beyond a prefix listing.
- **Atomic:** Each incremental backup is a self-contained upload to a unique path. No read-modify-write of shared metadata.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Per-backup metadata files (`.operator-meta/<name>.json`) | Adds a separate write-and-read artifact per backup. Metadata files must be kept in sync with actual backup data. Adds cloud SDK upload step to the sidecar (outside the xbcloud pipeline). The directory convention achieves the same discoverability with zero extra files. |
| CR-based chain linkage (`baseBackupName`, `previousBackupName` in CR status) | Works for in-place restore but breaks for cross-cluster restore — backup CRs don't exist on the target cluster. Would require users to manually specify all chain destinations for `backupSource` restores, which is poor UX. |
| Central registry file per cluster+storage (single file listing all backups) | Requires read-modify-write on every backup, creating concurrency risks. Corruption of the single file loses all metadata. |

### 5.3 LSN Retrieval

**Chosen approach:** The sidecar exposes a `/backup/checkpoint-info` endpoint. When the controller needs the `to_lsn` for the next incremental, it calls this endpoint with the previous backup's storage config and destination. The sidecar downloads `xtrabackup_checkpoints` via `xbcloud get`, parses it, and returns a `CheckpointInfo` struct. The LSN is **not** persisted in the backup CR status.

**Why:** The checkpoints file is already uploaded as part of the backup stream. The on-demand fetch avoids adding new status fields to the CRD and keeps the CR simpler. The storage round-trip is small (a few KB file) and only happens once per incremental backup creation.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Parse xtrabackup stderr for LSN log lines | Fragile — log format may change across PXB versions |
| Intercept `xtrabackup_checkpoints` from the xbstream as it flows through the pipe | Adds complexity to the streaming pipeline; risk of subtle bugs in stream interception |
| Store `toLsn` in backup CR status and read it from the CR | Adds a new CRD field; the on-demand fetch from storage is equally reliable and avoids CRD expansion |

### 5.4 Same-Storage Constraint for Chain Members

**Chosen approach:** All backups in a chain must use the same `storageName`.

**Why:** While there is no technical reason backup data can't live on different backends, supporting mixed-storage chains adds significant complexity to the restore path:
- The restore Job currently receives a single set of storage credentials. Mixed-storage chains would require multiple credential sets.
- The restore script would need to switch between S3/GCS/Azure download functions per chain member.
- Validation would need to verify connectivity to every distinct storage backend before starting.

The added complexity is not justified. Users needing different storage tiers should use S3 lifecycle policies or similar storage-layer tiering.

### 5.5 Chain Resolution: Automatic with Optional Explicit Base

**Chosen approach:** By default, the operator automatically resolves the most recent succeeded full backup for the same `clusterName` + `storageName`. Optionally, users can specify an explicit base via `spec.incrementalBaseBackupName`.

**Why:** Auto-resolution simplifies the common case (weekly full + daily incremental schedule), where the correct base is unambiguous. The optional explicit base (`incrementalBaseBackupName`) supports advanced use cases where users need control over which chain an incremental belongs to. A CRD validation rule ensures `incrementalBaseBackupName` is only used with `type: incremental`.

### 5.6 Scheduled Incremental Without a Full Base

**Chosen approach:** Skip the scheduled incremental with a warning event if no succeeded full backup exists.

**Why:** Silently promoting to a full backup changes expected behavior and storage consumption. A warning gives the user clear signal to create a full backup first. See Open Question 2 for discussion of making this configurable.

---

## 6. User Experience

### 6.1 Existing CR (Unchanged)

```yaml
# Full backup — identical to today. type defaults to "full".
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: my-full-backup
spec:
  clusterName: my-cluster
  storageName: s3-us
```

### 6.2 On-Demand Incremental Backup (Auto-Resolved Base)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: my-inc-backup-1
spec:
  clusterName: my-cluster
  storageName: s3-us
  type: incremental
  # Operator auto-resolves the most recent succeeded full backup
  # for my-cluster + s3-us as the base of the chain.
```

### 6.3 On-Demand Incremental Backup (Explicit Base)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: my-inc-backup-1
spec:
  clusterName: my-cluster
  storageName: s3-us
  type: incremental
  incrementalBaseBackupName: my-full-backup
  # Uses the specified full backup as the base of the chain.
  # CRD validation ensures incrementalBaseBackupName is only set with type: incremental.
```

### 6.4 Scheduled Full + Incremental

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: my-cluster
spec:
  backup:
    schedule:
      - name: weekly-full
        schedule: "0 0 * * 0"       # Sunday midnight
        keep: 4
        storageName: s3-us
        type: full                   
      - name: daily-incremental
        schedule: "0 0 * * 1-6"     # Mon-Sat midnight
        keep: 24
        storageName: s3-us
        type: incremental
```

### 6.5 Restore from Incremental (In-Place)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore-to-inc2
spec:
  clusterName: my-cluster
  backupName: my-inc-backup-2
  # Operator checks backup type is incremental, calls resolveIncrementalChain():
  #   parses destination (e.g., "prefix/cluster-ts-full.incr/cluster-ts-incr"),
  #   lists the ".incr/" prefix to discover the chain,
  #   and restores: full → inc1 → inc2
```

### 6.6 Cross-Cluster Restore from Incremental

```yaml
# Restore to a new cluster using backupSource — no backup CRs needed.
# The operator discovers the chain from the ".incr/" directory convention.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore-to-new-cluster
spec:
  clusterName: new-cluster
  backupSource:
    destination: s3://bucket/prod/backups/cluster-2026-03-15-full.incr/cluster-2026-03-17T000000-incr
    storage:
      type: s3
      s3:
        bucket: my-bucket
        credentialsSecret: s3-credentials
        region: us-west-2
    # Operator parses destination:
    #   base = s3://bucket/prod/backups/cluster-2026-03-15-full
    #   lists s3://bucket/prod/backups/cluster-2026-03-15-full.incr/ for chain members
```

---

## 7. Error Handling and Edge Cases

### 7.1 Chain Breakage (Mid-Chain Deletion)

**Scenario:** A user attempts to delete a mid-chain backup.

**Expected behavior:**
- The `percona.com/delete-backup` finalizer **prevents deletion of mid-chain incremental backups**. Only the latest incremental in a chain can be deleted.
- When a **full (base) backup** is deleted, the `percona.com/delete-backup` finalizer **cascade-deletes all dependent incremental backups**.
- If storage data is externally deleted or corrupted (outside the operator), chain breakage is detected at **restore time**: the restore controller lists the `.incr/` prefix and validates that all chain member destinations exist before starting.

### 7.2 Failed Incremental Backup

**Scenario:** An incremental backup fails mid-execution.

**Expected behavior:**
- The chain remains valid up to the last succeeded backup.
- The failed backup does not break the chain — it simply didn't extend it.
- The next incremental references the last succeeded backup, not the failed one.
- The controller skips failed backups when resolving the chain.

### 7.3 Concurrent Backups on Same Chain

**Scenario:** Two incremental backups targeting the same chain run concurrently.

**Constraint:** The sidecar already prevents concurrent backups (atomic bool check).

**Rationale:** Concurrent incremental backups would produce two backups with the same `from_lsn`, creating an ambiguous chain fork.

### 7.4 No Full Backup Exists for Incremental

**Scenario:** User creates an incremental backup but no succeeded full backup exists for the same `clusterName` + `storageName`.

**Expected behavior:**
- Validation rejects the incremental backup with a descriptive error: "no succeeded full backup found for cluster X with storage Y".
- For scheduled incrementals, the backup is skipped with a warning event (see Decision 5.6).

### 7.5 Storage Mismatch

**Scenario:** Incremental backup references a chain whose full backup uses a different storage backend.

**Constraint:** All backups in a chain must use the same `storageName`. Enforced at CR validation time.

**Rationale:** See Decision 5.4.

---

## 8. Migration and Backward Compatibility

### 8.1 Existing Clusters

- All existing `PerconaServerMySQLBackup` CRs have no `type` field — treated as `full` (CRD default).
- Existing backups continue to work unchanged.
- Existing restore workflows are fully backward compatible (empty `BACKUP_INCREMENTALS_DEST` env var triggers the existing full restore code path).

### 8.2 CRD Compatibility

- All changes are additive: new optional fields with sensible defaults.
- `type` defaults to `full`.
- `incrementalBaseBackupName` is optional and validated to only work with `type: incremental`.
- No breaking changes to existing CRD schema.

### 8.3 Operator Version Skew

- **New operator, old sidecar:** The new `incrementalLsn` field in BackupConfig is ignored by old sidecars (standard JSON unmarshaling). Incremental backups will fail because the sidecar won't pass `--incremental-lsn` to xtrabackup. The `/backup/checkpoint-info` endpoint won't exist, so LSN retrieval will also fail.
- **Old operator, new sidecar:** New sidecars are backward compatible — empty `incrementalLsn` means full backup. The checkpoint endpoint is additive and won't be called by old operators.

### 8.4 Legacy Backups

Backups taken before this feature use plain destination paths without the `.incr/` convention. At restore time, the restore controller parses the destination — if it does not contain `.incr/`, it is treated as a standalone full backup and the existing restore logic runs unchanged. This ensures full backward compatibility with no special-casing needed.

---

## 9. Testing Strategy

### 9.1 E2E Test Scenarios

| Scenario | Cluster Type | What It Validates |
|----------|-------------|-------------------|
| Full backup unchanged | GR + Async | Existing full backup workflow has no regressions with the new CRD fields present |
| Basic incremental chain | GR + Async | Full → Inc1 → Inc2 all succeed; status fields (type, destination) and annotations (base-backup-name) are populated correctly |
| Restore from full in a chain | GR + Async | Restore from the full backup in a chain ignores incrementals and restores correctly |
| Restore from incremental | GR + Async | Full → Inc1 → Inc2, restore from Inc2 — all data including post-incremental writes is present |
| Restore from mid-chain | GR + Async | Full → Inc1 → Inc2, restore from Inc1 — only base + inc1 data is present |
| Cross-cluster restore | GR + Async | Restore incremental chain to a new cluster via `backupSource` — operator discovers chain from `.incr/` directory convention |

---

## 10. Open Questions

1. Should we enforce a maximum number of incrementals in a chain?
   - **Resolution:** No

2. If a scheduled incremental finds no valid full base, should it automatically take a full backup instead?
   - *Option A:* Skip with warning.
   - *Option B:* Auto-promote to full backup.
   - **Status:** Scheduled incremental backups are not yet implemented (`BackupSchedule` has no `type` field).

3. When `keep=4` on full backups with dependent incrementals, should we count chains or individual backups?
   - *Option A:* Count individual backup CRs.
   - *Option B:* Count chains (keep 4 full backups and all their incrementals).
   - **Status:** Current implementation cascade-deletes all incrementals when a full backup is deleted via `deleteIncrementalChain()`. Retention counting behavior TBD.

---

## Appendix

### A. Glossary

| Term | Definition |
|------|------------|
| LSN | Log Sequence Number — InnoDB's internal counter for page modifications |
| PXB | Percona XtraBackup — the backup tool used by the operator |
| xbstream | PXB's streaming format for backup data |
| xbcloud | PXB's tool for uploading/downloading xbstream data to/from cloud storage |
| Chain | An ordered sequence of backups: one full (base) followed by zero or more incrementals |
| Delta files | `.delta` files produced by incremental backups, containing only changed pages |

### B. References

- [Percona XtraBackup Incremental Backup Documentation](https://docs.percona.com/percona-xtrabackup/8.4/create-incremental-backup.html)
- [xtrabackup_checkpoints File Reference](https://docs.percona.com/percona-xtrabackup/8.4/xtrabackup-files.html)
- [Percona XtraBackup Page tracking component](https://docs.percona.com/percona-xtrabackup/8.4/page-tracking.html)


