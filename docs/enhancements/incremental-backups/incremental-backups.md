# K8SPS-410: Incremental Backup Support

| Field        | Value              |
|--------------|--------------------|
| Author       | @mayankshah1607    |
| Status       | Draft              |
| Created      | 2026-03-18         |
| Last Updated | 2026-03-18         |
| Reviewers    | [names]            |

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

2. **LSN must be persisted.** The `to_lsn` from `xtrabackup_checkpoints` must be captured after each backup and stored in the backup CR status so subsequent incremental backups can reference it.

3. **Restore requires all chain members.** The restore process must download the full base backup plus all incremental backups in the chain and apply them in order.

4. **Chain integrity is critical.** If any backup in the chain is deleted or corrupted, all subsequent incrementals become unrestorable.

5. **Backup destination paths.** Each backup gets a unique destination path. Incremental backups need separate paths but must be logically linked.

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
User creates PerconaServerMySQLBackup CR with backupType: incremental
  → Backup Controller reconciles
    → Resolves incremental chain: finds latest full backup, walks chain,
      extracts to_lsn from most recent succeeded chain member
    → Labels CR with base-backup and backup-type for efficient querying
    → Creates K8s Job (run-backup.sh)
      → Job POSTs BackupConfig (now includes backupType + incrementalLsn)
        → Sidecar runs: xtrabackup --backup --stream=xbstream
            --incremental-lsn=<LSN> | xbcloud put
    → Controller fetches xtrabackup_checkpoints from sidecar, captures LSN
    → Updates CR status with LSN, chain metadata
```

**Restore flow (incremental):**
```
User creates PerconaServerMySQLRestore CR referencing an incremental backup
  → Restore Controller reconciles
    → Walks chain backward via PreviousBackupName to build ordered list:
      [full, inc1, inc2, ..., incN]
    → Validates all chain members exist and are Succeeded
    → Pauses cluster
    → Creates K8s Job with chain info (base dest + ordered incremental dests)
      → Job downloads and extracts base backup
      → Prepares base with --apply-log-only
      → For each incremental: download, extract, apply with --apply-log-only
        (except the last one, which omits --apply-log-only)
      → Move back to datadir
    → Unpauses cluster
```

### 3.3 Key Observations

1. **Sidecar is stateless.** The sidecar has no persistent storage and no knowledge of previous backups. All chain state must live in Kubernetes CRs, and the LSN must be passed explicitly to the sidecar via the BackupConfig.

2. **Controller is the chain coordinator.** The backup controller is the only component with visibility across all backup CRs. It must resolve chains and pass the correct LSN to the sidecar.

3. **Restore Job needs multi-backup awareness.** Today the restore Job handles a single backup. Incremental restore requires downloading and applying multiple backups in sequence, with careful ordering and prepare flags.

4. **Existing sidecar concurrency guard.** The sidecar already prevents concurrent backups via an atomic bool check. This naturally prevents two incremental backups from racing on the same chain.

---

## 4. CRD and Interface Changes

### 4.1 CRD Spec Changes

- **`PerconaServerMySQLBackup.spec.backupType`** *(optional, default: `"full"`)*:
  Specifies whether this is a `full` or `incremental` backup. When set to `incremental`, the operator automatically resolves the most recent succeeded full backup for the same `clusterName` + `storageName` as the chain base. Validation rejects `incremental` if no succeeded full backup exists for the matching cluster and storage.

- **`BackupSchedule.backupType`** *(optional, default: `"full"`)*:
  Specifies the backup type for scheduled backups. Must be one of `[full, incremental]`. When `incremental`, the operator auto-resolves the latest succeeded full backup for the same `storageName` at the time each scheduled backup fires. If no full backup exists, the scheduled incremental is skipped with a warning (see Open Question 2 for alternative behavior).

- **No new restore spec fields.** When `backupName` references an incremental backup, the operator automatically resolves and restores the full chain (full → inc1 → ... → incN). This is the only valid behavior for incremental backups — an incremental backup cannot be restored in isolation. When `backupName` references a full backup, the existing restore logic runs unchanged.

### 4.2 CRD Status Changes

New fields on `PerconaServerMySQLBackupStatus`:

- **`backupType`**: Records whether this was a `full` or `incremental` backup.
- **`lsn`**: The log sequence number at the end of this backup (`to_lsn`). Used as the starting point for subsequent incremental backups.
- **`baseBackupName`**: Name of the full backup CR that anchors this chain. For full backups, this is the backup's own name.
- **`previousBackupName`**: Name of the backup CR this incremental is based on. Empty for full backups.

New kubebuilder print columns on `PerconaServerMySQLBackup`:

- `Type` (from `status.backupType`)
- `Base` (from `status.baseBackupName`, priority=1)

### 4.3 Internal Contracts

**BackupConfig (operator → sidecar).** Two new fields are added to the JSON payload POSTed to the sidecar's backup endpoint:

- `backupType`: `"full"` or `"incremental"`
- `incrementalLsn`: The LSN to pass as `--incremental-lsn` to xtrabackup. Only set when `backupType` is `"incremental"`.

Old sidecars ignore these fields (standard JSON unmarshaling). New sidecars treat empty fields as a full backup.

**Checkpoints endpoint (sidecar → operator).** A new sidecar HTTP endpoint returns parsed `xtrabackup_checkpoints` data after a backup completes. The controller calls this to capture the `to_lsn` for the backup CR status.

**Restore Job environment variables.** Three new env vars are passed to the restore Job container:

- `RESTORE_TYPE`: `"incremental"` or unset (full). When unset, the existing restore logic runs unchanged.
- `BACKUP_DEST_BASE`: Cloud storage path of the full (base) backup.
- `BACKUP_DEST_INCREMENTALS`: Comma-separated list of incremental backup destinations, in chain order.

### 4.4 User-Facing Behavior Changes

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

**Why:** Due to Constraint 1 (no local basedir available), the sidecar has no local copy of previous backups. The LSN is stored in the backup CR status and passed to the sidecar via BackupConfig.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Download previous backup's `xtrabackup_checkpoints` to a local dir and use `--incremental-basedir` | Adds unnecessary storage I/O and complexity; the LSN is a single integer that can be passed directly |

### 5.2 Chain Linkage: CR-Based vs. Storage Path-Based

**Chosen approach:** Chain linkage is maintained entirely in Kubernetes CRs via `baseBackupName` and `previousBackupName`. Each backup is stored as an independent object tree in cloud storage with no path-based coupling.

**Why:** Keeps the storage layer unchanged and avoids breaking backward compatibility with existing backup paths.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Nested storage paths (`<base-backup-name>/full/`, `<base-backup-name>/inc1/`) | Changes existing storage path convention, breaking backward compatibility. Couples chain semantics to storage layout. Makes `xbcloud get` invocations more complex. CR-based linkage is more flexible and queryable. |

### 5.3 LSN Capture Method

**Chosen approach:** After backup upload completes, the sidecar fetches the `xtrabackup_checkpoints` file from cloud storage via `xbcloud get`, parses it, and exposes the values via a new HTTP endpoint.

**Why:** Simplest and most reliable. The checkpoints file is already uploaded as part of the backup stream. A post-backup download adds one small storage round-trip but avoids modifying the streaming pipeline.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Parse xtrabackup stderr for LSN log lines | Fragile — log format may change across PXB versions |
| Intercept `xtrabackup_checkpoints` from the xbstream as it flows through the pipe | Adds complexity to the streaming pipeline; risk of subtle bugs in stream interception |

### 5.4 Same-Storage Constraint for Chain Members

**Chosen approach:** All backups in a chain must use the same `storageName`.

**Why:** While there is no technical reason backup data can't live on different backends, supporting mixed-storage chains adds significant complexity to the restore path:
- The restore Job currently receives a single set of storage credentials. Mixed-storage chains would require multiple credential sets.
- The restore script would need to switch between S3/GCS/Azure download functions per chain member.
- Validation would need to verify connectivity to every distinct storage backend before starting.

The added complexity is not justified. Users needing different storage tiers should use S3 lifecycle policies or similar storage-layer tiering.

### 5.5 Chain Resolution: Automatic vs. Explicit Base

**Chosen approach:** The operator automatically resolves the most recent succeeded full backup for the same `clusterName` + `storageName`. Users do not specify a base backup.

**Why:** Simplifies the user experience. In the common case (weekly full + daily incremental schedule), the correct base is unambiguous. Explicit base specification can be added later if needed without breaking changes.

**Alternatives considered:**

| Alternative | Why Rejected |
|-------------|--------------|
| Require `baseBackupName` in incremental spec | Adds friction to the common case; error-prone for scheduled backups where the base changes over time |

### 5.6 Scheduled Incremental Without a Full Base

**Chosen approach:** Skip the scheduled incremental with a warning event if no succeeded full backup exists.

**Why:** Silently promoting to a full backup changes expected behavior and storage consumption. A warning gives the user clear signal to create a full backup first. See Open Question 2 for discussion of making this configurable.

---

## 6. User Experience

### 6.1 Existing CR (Unchanged)

```yaml
# Full backup — identical to today. backupType defaults to "full".
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: my-full-backup
spec:
  clusterName: my-cluster
  storageName: s3-us
```

### 6.2 On-Demand Incremental Backup

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: my-inc-backup-1
spec:
  clusterName: my-cluster
  storageName: s3-us
  backupType: incremental
  # Operator auto-resolves the most recent succeeded full backup
  # for my-cluster + s3-us as the base of the chain.
```

### 6.3 Scheduled Full + Incremental

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
        backupType: full
      - name: daily-incremental
        schedule: "0 0 * * 1-6"     # Mon-Sat midnight
        keep: 24
        storageName: s3-us
        backupType: incremental
        # No baseBackupName needed — operator auto-resolves
```

### 6.4 Restore from Incremental

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: restore-to-inc2
spec:
  clusterName: my-cluster
  backupName: my-inc-backup-2
  # Operator detects that my-inc-backup-2 is incremental and automatically
  # resolves and restores the full chain: full → inc1 → inc2
```

---

## 7. Error Handling and Edge Cases

### 7.1 Chain Breakage (Mid-Chain Deletion)

**Scenario:** A mid-chain backup is deleted or corrupted.

**Expected behavior:**
- Block deletion of mid-chain backups via finalizer. If the user attempts to delete a backup that has successors, the operator rejects the deletion with a clear error.
- Allow tail deletion (most recent backup in a chain).
- Allow cascade deletion: deleting the base (full) backup deletes all dependent incrementals.
- If a backup is externally deleted (e.g., S3 object removed outside the operator), the chain breakage is detected at restore time when the restore controller validates all chain members.

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

- All existing `PerconaServerMySQLBackup` CRs have no `backupType` field — treated as `full`.
- No migration needed; existing backups continue to work unchanged.
- Existing restore workflows are fully backward compatible (empty `RESTORE_TYPE` env var triggers the existing restore code path).

### 8.2 CRD Compatibility

- All changes are additive: new optional fields with sensible defaults.
- `backupType` defaults to `full`.
- No breaking changes to existing CRD schema.
- Requires `make generate manifests` to regenerate CRDs and deepcopy.

### 8.3 Operator Version Skew

- **New operator, old sidecar:** The new `backupType` and `incrementalLsn` fields in BackupConfig are ignored by old sidecars (standard JSON unmarshaling). Incremental backups will fail because the sidecar won't pass `--incremental-lsn` to xtrabackup. The controller should handle 404 from the checkpoints endpoint gracefully.
- **Old operator, new sidecar:** New sidecars are backward compatible — empty incremental fields mean full backup.

---

## 9. Testing Strategy

### 9.1 E2E Test Scenarios

| Scenario | Cluster Type | What It Validates |
|----------|-------------|-------------------|
| Full backup unchanged | GR + Async | Existing full backup workflow has no regressions with the new CRD fields present |
| Basic incremental chain | GR + Async | Full → Inc1 → Inc2 all succeed; status fields (LSN, chain metadata) are populated correctly |
| Restore from full in a chain | GR + Async | Restore from the full backup in a chain ignores incrementals and restores correctly |
| Restore from incremental | GR + Async | Full → Inc1 → Inc2, restore from Inc2 — all data including post-incremental writes is present |
| Restore from mid-chain | GR + Async | Full → Inc1 → Inc2, restore from Inc1 — only base + inc1 data is present |
| Scheduled full + incremental | GR + Async | Cron creates full weekly + daily incrementals; chain linkage labels and status fields are correct |
| Retention with chains | GR + Async | Deleting old full backups cascades to their dependent incrementals |
| Validation failures | GR + Async | Incremental without full, wrong storage, chain gap — all rejected with descriptive errors |
| Failed incremental recovery | GR + Async | Inc fails, next inc references last succeeded backup and completes successfully |
| Concurrent incremental rejection | GR + Async | Two incrementals for same chain — second is rejected |
| Storage backends | GR + Async | Basic incremental test repeated on S3, GCS, and Azure |

---

## 10. Open Questions

1. Should we enforce a maximum number of incrementals in a chain?
   - *Option A:* No limit.
   - *Option B:* Configurable limit with a default of 7 (weekly full + daily incrementals).

2. If a scheduled incremental finds no valid full base, should it automatically take a full backup instead?
   - *Option A:* Skip with warning.
   - *Option B:* Auto-promote to full backup.

3. When `keep=4` on full backups with dependent incrementals, should we count chains or individual backups?
   - *Option A:* Count individual backup CRs.
   - *Option B:* Count chains (keep 4 full backups and all their incrementals).

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
