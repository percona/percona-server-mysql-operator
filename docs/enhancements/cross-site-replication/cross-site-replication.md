# [K8SPS-508]: Cross-Site Replication


| Field        | Value           |
| ------------ | --------------- |
| Author       | @mayankshah1607 |
| Status       | Draft           |
| Created      | 2026-05-11      |
| Last Updated | 2026-05-11      |
| Reviewers    |                 |


---

## 1. Overview

This feature adds asynchronous cross-site replication to the Percona Operator for MySQL. A *replica* cluster declares one or more MySQL replication channels in `spec.mysql.replicationChannels`; each channel pulls binary log events from one or more endpoints belonging to a *source* cluster. Replication is driven by MySQL 8.0.22+ native asynchronous connection failover.

The feature targets disaster-recovery, geo-distributed read serving, and cross-region migration use cases.

### 1.1 Goals

- A single new field, `spec.mysql.replicationChannels`, on the existing `PerconaServerMySQL` CRD configures the replica side of cross-site replication.
- Works for both `clusterType: group-replication` and `clusterType: async`.
- Topology: **M sources → N replicas**. Each replica CR declares one or more named replication channels; each channel can list multiple source endpoints with weights.
- MySQL-native source failover via `SOURCE_CONNECTION_AUTO_FAILOVER = 1`. For GR sources, `asynchronous_connection_failover_add_managed()` tracks membership changes inside the source cluster automatically.
- Replica enforces `super_read_only = ON` on its primary while replicating, to prevent application split-brain.
- Promotion is user-driven by editing the CR (removing the channels). No automatic cross-site failover.
- TLS for the replication channel is on by default; disabling it requires an explicit `unsafeFlags` opt-in.

### 1.2 Non-Goals (Out of Scope)

- **Operator-managed initial seeding.** The user is responsible for bootstrapping the replica cluster (typically via a `PerconaServerMySQLRestore` from a backup of the source) *before* setting `replicationChannels`.
- **Bidirectional / active-active replication** — significant conflict resolution work; revisit once unidirectional cross-site is stable.
- **Chained replication** (A → B → C) — every additional hop multiplies the failure modes; out of initial scope.
- **Automatic cross-site failover** — split-brain risk under WAN partitions is too high for an initial release. Manual promotion only.
- **Replication filters in the CRD** (`replicate-do-db`, `replicate-ignore-`*) — multi-source replication works correctly when sources have non-overlapping schemas; we document that constraint rather than exposing per-channel filters in v1.

---

## 2. Background

### 2.1 Core Concepts

**MySQL asynchronous replication.** A replica server's IO thread reads binary log events from a source over a TCP connection, persists them to a relay log, and a SQL thread applies them locally.

**Global Transaction Identifiers (GTID).** Every transaction is tagged with `<server_uuid>:<transaction_id>`. `server_uuid` is auto-generated and unique per MySQL instance. GTID-based replication (`SOURCE_AUTO_POSITION = 1`) removes the need for the operator to track binlog file/position; the replica asks for "everything I haven't applied yet."

**Multi-source replication.** A replica can have multiple *channels*, each with its own IO thread, SQL thread, GTID state, source endpoints, and failover config. Each channel is keyed by `channel_name` in the performance/replication tables.

**Native asynchronous connection failover** (MySQL 8.0.22+). When a channel is created with `SOURCE_CONNECTION_AUTO_FAILOVER = 1`, the replica's IO thread reads source endpoints from `mysql.replication_asynchronous_connection_failover` (populated by `asynchronous_connection_failover_add_source()` or `_add_managed()`). MySQL picks the highest-weight reachable source and fails over automatically when it becomes unavailable, without any operator involvement. For a managed GR source, the failover list reflects current GR membership; member changes propagate without the operator re-asserting anything.

`**log_slave_updates` and the source binlog stream.** Every pod in an operator-managed cluster runs with `log_slave_updates = ON` and `log_bin = ON`. This means every pod — primary or in-cluster replica — emits the same binlog stream. Cross-site replication can fetch binlogs from any pod in a source cluster, not only the writable primary.

**Per-pod Service exposure.** The operator already supports `spec.mysql.expose` to expose each pod through its own external Service (LoadBalancer/NodePort). This is the mechanism by which source-cluster pods become individually reachable from outside the source's Kubernetes cluster.

`**super_read_only`.** A server-level flag that prevents writes from all clients, including users with `SUPER` privilege. The cross-site reconciler sets `super_read_only = ON` on the replica's primary while replicating, and clears it when channels are removed.

### 2.2 Key Constraints

1. **MySQL 8.0.22 or later.** Required for `SOURCE_CONNECTION_AUTO_FAILOVER` and the failover-table UDFs. Both supported MySQL versions (8.0, 8.4) include these features.
2. **Reachability between clusters.** Replica must reach at least one source pod's exposed endpoint over TCP. The operator does not solve cross-cluster networking — that is the user's responsibility (LB hostnames, firewall rules, ServiceMesh, VPN).
3. **The user is responsible for the initial seed.** Before setting `spec.mysql.replicationChannels`, the user must ensure the replica cluster contains a consistent snapshot of the source's data — typically by:
  1. Taking a `PerconaServerMySQLBackup` of the source.
  2. Restoring it into the replica cluster via `PerconaServerMySQLRestore` (using `backupSource` if the replica is in a different Kubernetes cluster).
  3. Verifying the restore is complete.
  4. Only then setting `replicationChannels` on the replica CR.
4. **A new `replication` MySQL system user is added.** The operator provisions it automatically on every cluster (alongside the existing system users like `root`, `xtrabackup`, `monitor`, etc.) with `REPLICATION SLAVE` and `REPLICATION CLIENT` grants and no other privileges. Its password lives under the `replication` key in `spec.secretsName`. On clusters where cross-site is not in use, the user simply exists unused — the grants are read-only on the replication stream and pose no risk. The replica admin reads this password from the source's Secret, places it into a Secret in the replica's namespace, and references that via `replicationUserSecretRef`. The cross-site reconciler itself does not create the user. If the replica cluster is setup with the same `replication` password as the source, this `replicationUserSecretRef` may be skipped.
5. `**super_read_only` is the only fence.** No quorum, witness, or fencing service. Split-brain protection relies on the operator's invariant that the replica's primary is read-only while `replicationChannels` is non-empty.
6. **Backward compatibility.** Existing CRs (without `replicationChannels`) must continue to work as usual (no changes).

---

## 3. Architecture

### 3.1 Architecture Before This Change

```
PerconaServerMySQL CR
  → ps reconciler
    → reconciles StatefulSet (mysql pods)
    → reconciles HAProxy/Router (proxy)
    → reconciles Orchestrator (async clusterType)
    → reconciles Services (incl. per-pod Services when spec.mysql.expose set)
    → reconciles system users via spec.secretsName
    → backup/restore via PerconaServerMySQLBackup / Restore controllers
```

No concept of replication beyond cluster boundaries.

### 3.2 Architecture After This Change

The changes mainly touch the replica side. A source cluster is just a normal cluster with:

- `spec.mysql.expose` configured for external reach,
- a `replication` key in its `spec.secretsName` Secret (so the existing user-management code provisions the MySQL user).

The replica cluster reconciler grows a new phase that activates when `spec.mysql.replicationChannels` is non-empty:

```
ps reconciler
  ├─ phase: channel reconcile (on the cluster's current primary pod)
  │    for each declared channel:
  │      populate mysql.replication_asynchronous_connection_failover
  │        (asynchronous_connection_failover_add_managed for GR sources,
  │         asynchronous_connection_failover_add_source for async sources)
  │      CHANGE REPLICATION SOURCE TO
  │        SOURCE_USER='replication',
  │        SOURCE_PASSWORD=<from replicationUserSecretRef>,
  │        SOURCE_AUTO_POSITION=1,
  │        SOURCE_CONNECTION_AUTO_FAILOVER=1,
  │        SOURCE_SSL=<config.tls ? 1 : 0>,
  │        SOURCE_SSL_VERIFY_SERVER_CERT=<config.tlsSkipVerify ? 0 : 1>,
  │        SOURCE_SSL_CA=<from config.caSecret>,
  │        SOURCE_RETRY_COUNT=<config.sourceRetryCount>,
  │        SOURCE_CONNECT_RETRY=<config.sourceConnectRetry>
  │        FOR CHANNEL '<name>'
  │      START REPLICA FOR CHANNEL '<name>'
  │    SET PERSIST super_read_only = ON
  │
  ├─ phase: drift reconciliation (every loop)
  │    diff declared channels against SHOW REPLICA STATUS
  │    add/remove/update channels and failover rows as needed
  │    if the cluster's primary moved: re-assert all channels on the new
  │      primary; RESET REPLICA ALL on the previous primary
  │
  └─ phase: teardown (when replicationChannels removed)
       STOP REPLICA; RESET REPLICA ALL
       SET PERSIST super_read_only = OFF
```

### 3.3 Key Observations

1. **The cross-site channel always runs on the replica cluster's current primary.** Other replica pods receive data via in-cluster replication (GR or async). The operator's main correctness invariant is keeping the channel attached to whatever pod is currently primary.
2. **All cross-site state is per-cluster and recoverable from MySQL.** The operator stores no out-of-band state; on every reconcile it can rebuild its understanding from `SHOW REPLICA STATUS` plus the CR.
3. **The source side has no cross-site-specific reconciler logic.** A source is a cluster with exposed pods and a `replication` user; nothing in the CR explicitly marks it as a source. This keeps fan-out free and removes coupling between source and replica CRs.
4. **MySQL's native auto-failover replaces operator-side primary discovery.** The replica reconciler never polls source endpoints to find a writable primary; MySQL's IO thread does that.
5. **Bootstrap is user-driven.** The reconciler never invokes restore code paths. If the user mis-bootstraps the replica, MySQL itself rejects the IO thread on `START REPLICA` with a GTID-divergence error (typically MySQL error 1236); the operator surfaces that error verbatim. There is no operator-side pre-flight check.

---

## 4. CRD and Interface Changes

### 4.1 CRD Spec Changes

A new optional field, `replicationChannels`, is added under `spec.mysql`:

```yaml
spec:
  mysql:
    clusterType: group-replication
    size: 3
    replicationChannels:
      - name: ch_primary                    # required, non-empty, unique
                                            # within the list,
                                            # [a-zA-Z0-9_]+, ≤64 chars.
        replicationUserSecretRef:
          name: source-replication-creds    # Secret in this namespace.
          key: replication                  # default "password".
        sources:
          - host: cluster1-mysql-0.example.com
            port: 3306
            weight: 100                     # default 100. Range [0, 100].
          - host: cluster1-mysql-1.example.com
            port: 3306
            weight: 50
        config:
          sourceRetryCount: 3               # default 3.
          sourceConnectRetry: 60            # default 60.
          tls: true                         # default true.
          tlsSkipVerify: false              # default false.
          caSecret:                         # optional. Required only when
                                            # the source's TLS cert is not
                                            # signed by a system-trusted CA
                                            # and tlsSkipVerify is false.
            name: source-ca
            key: ca.crt
```

#### 4.1.1 Field semantics

- `**replicationChannels**` *(list, optional, default empty)*. Non-empty list declares the cluster a replica. The operator will reconcile cross-site channels on whichever pod is currently the cluster's primary. Maximum 16 channels (CEL-validated; practical ceiling tied to applier worker defaults; can be raised later).
- `**replicationChannels[].name`** *(string, required)*. MySQL channel name. Must match `[a-zA-Z0-9_]+`, length 1–64. Must be unique within the list. Renaming a channel is observed by the reconciler as remove-old + add-new.
- `**replicationChannels[].replicationUserSecretRef`** *(SecretKeySelector, optional)*. References a Secret in the same namespace as the replica CR. Reads the password for the `replication` MySQL user from the named key (default `password`). The IO thread always authenticates as `replication` — there is no field to override the username. The Secret is watched; updates trigger a `CHANGE REPLICATION SOURCE TO ... SOURCE_PASSWORD=<new>` reissue for the affected channel. May be omitted when the replica's own `spec.secretsName` already holds the same password as the source under the `replication` key, in which case the operator authenticates with the local secret.
- `**replicationChannels[].sources`** *(list, required, non-empty)*. Endpoints fed into the channel's failover table. The user may list any source-cluster pod endpoints — primary or in-cluster replica. MySQL picks one based on weight and reachability.
- `**replicationChannels[].sources[].host` / `.port`** *(string / int, required)*. Externally reachable host:port. Typically derived from the source's per-pod LoadBalancer/NodePort hostnames.
- `**replicationChannels[].sources[].weight`** *(int, default `100`, range `[0, 100]`)*. Forwarded to the failover table. Higher = preferred. Weight `0` is a valid "blacklist" value — endpoint is tried only when all others are unreachable.
- `**replicationChannels[].config`** *(object, optional)*. Per-channel knobs for retry behavior and TLS. All fields default sensibly; an entire channel can omit the block.
- `**replicationChannels[].config.sourceRetryCount` / `.sourceConnectRetry`** *(uint32)*. Forwarded into `CHANGE REPLICATION SOURCE TO`. Defaults from `pkg/mysql` constants (`DefaultAsyncSourceRetryCount`, `DefaultAsyncSourceConnectRetry`).
- `**replicationChannels[].config.tls`** *(bool, default `true`)*. Sets `SOURCE_SSL = 1` on the channel when true. Disabling requires `unsafeFlags.crossSiteReplicationTLS`.
- `**replicationChannels[].config.tlsSkipVerify`** *(bool, default `false`)*. When true, sets `SOURCE_SSL_VERIFY_SERVER_CERT = 0` — the IO thread encrypts the connection but does not validate the source's certificate. Useful for self-signed certs in lab/test environments; emits a `Warning` event in production. Ignored when `config.tls` is `false`.
- `**replicationChannels[].config.caSecret`** *(SecretKeySelector, optional)*. Reads a PEM CA bundle from the named Secret/key. When provided and `tlsSkipVerify` is `false`, the operator writes the bundle to a file in the replica's primary pod and passes its path as `SOURCE_SSL_CA`. Required when the source's TLS cert is not signed by a CA trusted by MySQL's default cert store. Ignored when `tlsSkipVerify` is `true` or `config.tls` is `false`.

#### 4.1.2 New UnsafeFlags key

```go
type UnsafeFlags struct {
    // ...existing...
    CrossSiteReplicationTLS bool `json:"crossSiteReplicationTLS,omitempty"`
}
```

#### 4.1.3 Validation rules (CEL on the CRD)

1. Channel names are unique, non-empty, match `[a-zA-Z0-9_]+`, length 1–64.
2. `replicationChannels[].sources` is non-empty for each channel.
3. `replicationChannels[].sources[].weight` is in `[0, 100]`.
4. Any `replicationChannels[].config.tls: false` requires `unsafeFlags.crossSiteReplicationTLS: true`.
5. `len(replicationChannels) ≤ 16`.
6. `replicationChannels` non-empty is mutually exclusive with `pause: true`.

There is intentionally no validation rule about the initial seed; that prerequisite is documented (Constraint 3) and surfaces as a MySQL-side runtime error (IO thread error 1236, surfaced in `lastError` with `reason = GTIDDivergence`) if violated.

### 4.2 CRD Status Changes

```yaml
status:
  replicationChannels:
    replicatingPod: cluster2-mysql-0      # pod currently running the
                                          # cross-site channels.
    channels:
      - name: ch_primary
        state: Running | Stopped | Error
        currentSource: cluster1-mysql-0.example.com:3306
        secondsBehindSource: 3            # null when IO thread not running
        ioThreadRunning: true
        sqlThreadRunning: true
        lastError: ""
        lastTransitionTime: 2026-05-11T...
```

`secondsBehindSource` is sourced from `SHOW REPLICA STATUS`'s `Seconds_Behind_Source`. It reports SQL-thread lag only.

A new top-level condition is added to the CR:

```yaml
- type: CrossSiteReplicating
  status: "True" | "False"
  reason: Running
        | SourceUnreachable
        | AuthenticationFailed
        | TLSError
        | GTIDDivergence
        | ReplicaPrimaryChanging
        | Stopped
  message: <details>
```

Aggregated: `True` only if every channel is `Running`.

### 4.3 Internal Contracts

- **New system user: `replication`.** Added to the operator's known system-users list (next to `root`, `xtrabackup`, `monitor`, etc.). Grants are deliberately minimal: `REPLICATION SLAVE` (required for the IO thread) and `REPLICATION CLIENT` (required for `SHOW REPLICA STATUS` and similar). No DML or DDL privileges. The password is generated on first reconcile if absent from `spec.secretsName`, stored under the key `replication`, and rotated when the Secret value rotates — exactly as today's system users behave. **Provisioning is gated on `spec.crVersion`.** The user is created only on clusters whose `crVersion` is ≥ the operator release that introduces this feature. Clusters at an older `crVersion` are left untouched — they retain their existing user inventory and `spec.secretsName` contents, so they do not see a Secret diff and do not trigger a rolling restart on operator upgrade. Users opt in by bumping `spec.crVersion` (already the documented upgrade path for adopting new features). Implementation touchpoints: extend the system-user list in `pkg/controller/ps/user.go` (with a `crVersion` predicate matching the existing pattern used for other version-gated user-management changes) and the password-generation path in `pkg/secret`. No reconciler logic is gated on cross-site state for this user — once the version gate passes, it's simply another row in the cluster's user inventory.
- **MySQL config invariants asserted by the reconciler:** `gtid_mode = ON`, `enforce_gtid_consistency = ON`, `log_slave_updates = ON`, `log_bin = ON`, `server_id` derived collision-safely. Already true today for both cluster types; the cross-site path verifies and refuses to start if any is off.
- `mysql.replication_asynchronous_connection_failover` is managed by the operator. Reconciler diffs declared sources against the table contents per channel and converges via `_add_source` / `_delete_source` UDFs (or `_add_managed` / `_delete_managed` for GR sources). Users editing the table directly may have their edits reverted on the next reconcile.

### 4.4 User-Facing Behavior Changes

- New events emitted: `CrossSiteReplicating`, `CrossSiteSourceUnreachable`, `CrossSiteAuthenticationFailed`, `CrossSiteTLSError`, `CrossSiteGTIDDivergence`, `CrossSiteReplicatingPodChanged`, `CrossSitePromoted`.
- No webhook/admission changes beyond the CEL rules above.

---

## 5. Design Decisions and Alternatives

### 5.1 Fields on the existing CR, under `spec.mysql`

**Chosen approach:** Add `replicationChannels` directly under `spec.mysql`, alongside existing per-cluster MySQL knobs.

**Why:** Configuration is per-cluster and works across separate Kubernetes clusters without cross-cluster CR references. Reuses existing primitives (per-pod expose, system-user provisioning). The placement under `spec.mysql` mirrors how other MySQL-scoped settings (`expose`, `clusterType`, `configuration`) live.

**Alternatives considered:**


| Alternative                                                                                    | Why Rejected                                                                                                                                                                              |
| ---------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| New top-level block (`spec.crossSiteReplication`) with an `enabled` flag and a `channels` list | Adds an `enabled` master switch that is redundant — presence of channels already implies activation. Adds a second tier of indentation in YAML.                                           |
| New CRD: `PerconaServerMySQLReplication` referencing two clusters by name                      | Cross-cluster references don't actually work across separate K8s clusters without federation. Two CRs to keep in sync per cluster. Heavier for a feature that is per-cluster declarative. |
| Reuse `clusterType: async`, no new spec                                                        | Conflates in-cluster async HA with inter-cluster replication. Orchestrator was not designed for external sources. Hostile to the GR path.                                                 |


### 5.2 Channel-shaped CRD with multi-source support

**Chosen approach:** `spec.mysql.replicationChannels` is a list from day one, and the controller supports multi-source replication.

**Why:** PXC operator parity. Users moving between Percona operators see a consistent shape. Multi-source consolidation is a real use case. The CRD shape directly mirrors MySQL's multi-source feature.

**Alternatives considered:**


| Alternative                                            | Why Rejected                                                                                                                                            |
| ------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Flat `sources` field, single channel only              | Simpler today, but a future multi-source need requires either a breaking change or a parallel field with mutual-exclusion validation. Loses PXC parity. |
| Channel-shaped CRD but single-channel enforced via CEL | Inherits the verbose shape without delivering the capability. Worst of both worlds.                                                                     |


### 5.3 MySQL-native asynchronous connection failover, not operator polling

**Chosen approach:** `CHANGE REPLICATION SOURCE … SOURCE_CONNECTION_AUTO_FAILOVER = 1` plus the failover table. For GR sources, use `asynchronous_connection_failover_add_managed()` so MySQL tracks the source cluster's membership directly.

**Why:** Source failover happens at MySQL's IO-thread level in milliseconds, not at operator-reconcile latency. No operator polling code, no `CHANGE REPLICATION SOURCE` re-issue on every in-source primary change.

**Alternatives considered:**


| Alternative                                                                                          | Why Rejected                                                                                                                                       |
| ---------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operator polls each endpoint to find writable primary, re-issues CHANGE REPLICATION SOURCE on change | Slower failover. More code. Worse availability semantics. Reinvents a MySQL-native feature.                                                        |
| Static `SOURCE_HOST` to a Service that points at the primary                                         | Service-update lag during in-source failover. Requires the operator to keep the Service endpoints in sync, same coordination cost, fewer benefits. |


### 5.4 Replica can fetch binlogs from any source pod, not just primary

**Chosen approach:** Allow users to list any pod endpoints in `replicationChannels[].sources`. MySQL picks based on weight and reachability.

**Why:** Every pod has `log_slave_updates = ON` and emits the same binlog stream. Replicating from an in-cluster replica adds at most one hop of lag and offloads the writable primary. Users may also prefer the geographically nearest pod regardless of role.

**Alternatives considered:**


| Alternative                                             | Why Rejected                                                                                              |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Operator enforces "primary only" by filtering endpoints | Throws away a real capability MySQL already provides. Imposes load on the writable primary unnecessarily. |


### 5.5 Manual, user-driven promotion

**Chosen approach:** Promotion happens by removing `replicationChannels` from the replica CR. The operator runs `STOP REPLICA; RESET REPLICA ALL` and clears `super_read_only`.

**Why:** Cross-region network blips are common. Automated failover risks split-brain. A declarative user edit is predictable, auditable, and reversible.

**Alternatives considered:**


| Alternative                                                             | Why Rejected                                                                                                               |
| ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Operator-orchestrated promotion API (`role: source` or `role: replica`) | Adds an imperative-feeling field on a declarative CRD. Same outcome as editing `replicationChannels` . Not enough benefit. |
| Automatic failover on source-unreachable detection                      | Too dangerous across WAN. Out of scope.                                                                                    |


### 5.6 User-driven bootstrap, not operator-mediated

**Chosen approach:** The user bootstraps the replica cluster with a consistent source snapshot, typically via `PerconaServerMySQLBackup` on the source and `PerconaServerMySQLRestore` on the replica, *before* setting `replicationChannels`. The operator does not invoke any restore machinery on the cross-site path.

**Why:**

- Keeps the cross-site reconciler narrow. It does one thing well, which is manage replication channels, and does not embed restore semantics, S3 error handling, or partial-seed recovery.
- Restore is already a deliberate, observable user action with its own CRD. Surfacing it through a hidden bootstrap field would duplicate controls and add silent failure modes (a misconfigured `bootstrap.backupSource` quietly burning S3 egress, for example).
- Users with the most common DR workflow are already using backup/restore on schedule; folding bootstrap into the cross-site CR saves no real work.

A mis-bootstrapped replica is caught at runtime by MySQL itself: `START REPLICA` triggers error (GTID divergence / missing binlogs on the source) and the operator surfaces it as `reason = GTIDDivergence`. The operator does no GTID comparison of its own.

**Alternatives considered:**


| Alternative                                                                                    | Why Rejected                                                                                                                                                                                                                     |
| ---------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operator creates an internal `PerconaServerMySQLRestore` from a `bootstrap.backupSource` field | Embeds a second copy of restore lifecycle in a different controller. Adds CR fields specific to S3/storage to the cross-site spec block. Adds failure modes (BootstrapFailed) that obscure the cross-site reconciler's core job. |


### 5.7 No source-side CR fields

**Chosen approach:** A source cluster has no cross-site-specific CR fields. It is a normal cluster with `spec.mysql.expose` configured and a `replication` key in its `spec.secretsName` Secret.

**Why:** The operator only needs cross-site logic on the replica side, since MySQL's native failover takes care of source-side primary tracking. Adding source-side fields would require either symmetric declarations (every source CR carries a flag) or replicas-list coupling (source enumerates its replicas) — both reintroduce coordination cost without delivering capability.

**Alternatives considered:**


| Alternative                                                                | Why Rejected                                            |
| -------------------------------------------------------------------------- | ------------------------------------------------------- |
| `spec.mysql.crossSiteSource: true` flag on the source CR                   | Pure declaration with no functional effect; UX clutter. |
| `replicas: [host:port, ...]` field on the source listing expected replicas | Requires cross-cluster knowledge on the source side     |


### 5.8 No replication filters in CRD

**Chosen approach:** Multi-source replication works only with non-overlapping schemas across channels. We will document this - we do not expose `replicate-do-db`/`replicate-ignore-db` in the CRD.

**Why:** Out of scope, users with more advanced needs can apply filters via `spec.mysql.configuration` if absolutely required.

### 5.10 Source-side scaling requires manual `sources` updates (async clusterType)

**Chosen approach:** For async-clusterType sources, the user updates each replica's `replicationChannels[].sources` list when the source cluster scales. For GR sources, `asynchronous_connection_failover_add_managed()` tracks GR membership and removes the manual step.

**Why:** Cross-cluster watches require complicated tooling. Documenting the manual step for the async path is acceptable; the GR path (more common) is already automatic.

---

## 6. Replication Model Impact

### 6.1 Group Replication source

- Replica side uses `asynchronous_connection_failover_add_managed()` with the source-cluster endpoint(s) provided in `replicationChannels[].sources`; MySQL queries the source's `performance_schema.replication_group_members` to maintain the failover list dynamically.
- In-source primary failover is invisible to the operator.
- Source-cluster scaling is invisible to the operator.

### 6.2 Group Replication replica

- Channels run on the GR primary only. On a GR primary change, the operator re-asserts channels on the new primary and runs `RESET REPLICA ALL FOR CHANNEL '<name>'` on the old primary.
- `super_read_only = ON` is set cluster-wide via in-cluster GR replication — the operator sets it on the primary, GR propagates the change.

### 6.3 Async source

- Replica side uses `asynchronous_connection_failover_add_source()` with the static endpoint list from `replicationChannels[].sources`.
- Source-cluster scaling requires updating `replicationChannels[].sources` on each replica.

### 6.4 Async replica

- Channels run on the async primary. On a primary change (orchestrator-driven), the operator re-asserts channels on the new primary as in the GR case.

### 6.5 Differences and Why

The GR path is more automatic because GR exposes membership via a documented performance-schema view that MySQL's failover mechanism consumes directly. Async clusters have no equivalent; Orchestrator manages topology but is not queryable by the replica's MySQL.

---

## 7. User Experience

### 7.1 Existing CR (unchanged)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: cluster1
spec:
  mysql:
    clusterType: group-replication
    size: 3
  # ... no replicationChannels: behaves identically to today.
```

### 7.2 Source cluster

A source cluster is a regular cluster with two operational requirements — neither of which is a CR field specific to cross-site:

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: source-cluster
spec:
  mysql:
    clusterType: group-replication
    size: 3
    expose:
      enabled: true                     # per-pod LoadBalancer Services.
      type: LoadBalancer
  # ... no replicationChannels — this cluster is not a replica.
```

The user's responsibilities on the source side:

1. Configure `mysql.expose` so the pods are reachable from outside.
2. Read the auto-generated `replication` user's password from the cluster's `spec.secretsName` Secret (key `replication`) and copy it into a Secret in the replica's namespace (used later via `replicationUserSecretRef`).
3. Take a backup of the source so the replica admin can seed from it.

The `replication` user itself is created automatically by the operator on every cluster (see 4.3); the source admin does not need to provision it manually.

### 7.3 Bootstrapping a new replica cluster (user steps, before `replicationChannels`)

```yaml
# 1. Take a backup of the source.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLBackup
metadata:
  name: source-bootstrap-backup
  namespace: source-ns
spec:
  clusterName: source-cluster
  storageName: s3-storage
---
# 2. Restore that backup into the (yet-to-be-replicating) target cluster.
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: replica-bootstrap-restore
  namespace: replica-ns
spec:
  clusterName: replica-cluster
  backupSource:
    destination: s3://my-bucket/backups/source-cluster/<timestamp>
    storage:
      type: s3
      s3:
        bucket: my-bucket
        region: us-east-1
        credentialsSecret: source-backup-creds
```

Once `replica-bootstrap-restore` reaches `Succeeded`, edit `replica-cluster` to add `replicationChannels` (see 7.4).

### 7.4 Replica cluster — single channel

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: replica-cluster
spec:
  mysql:
    clusterType: group-replication
    size: 3
    replicationChannels:
      - name: dr
        replicationUserSecretRef:
          name: source-replication-creds      # in this namespace. Alternatively, create cluster with this replication password
          key: password
        sources:
          - {host: source-mysql-0.example.com, port: 3306, weight: 100}
          - {host: source-mysql-1.example.com, port: 3306, weight: 100}
          - {host: source-mysql-2.example.com, port: 3306, weight: 100}
        config:
          tls: true
          caSecret:
            name: source-ca
            key: ca.crt
```

### 7.5 Replica cluster — multi-source consolidation

```yaml
spec:
  mysql:
    replicationChannels:
      - name: shard_us
        replicationUserSecretRef:
          name: shard-us-creds
          key: password
        sources:
          - {host: shard-us-mysql-0.example.com, port: 3306, weight: 100}
          - {host: shard-us-mysql-1.example.com, port: 3306, weight: 100}
      - name: shard_eu
        replicationUserSecretRef:
          name: shard-eu-creds
          key: password
        sources:
          - {host: shard-eu-mysql-0.example.com, port: 3306, weight: 100}
          - {host: shard-eu-mysql-1.example.com, port: 3306, weight: 100}
```

Constraint: `shard_us` and `shard_eu` must write to non-overlapping schemas (documented in 5.8). No filter is configured.

### 7.6 Promotion (replica → standalone)

Remove `replicationChannels` (or set it to an empty list). The operator runs `STOP REPLICA; RESET REPLICA ALL` and clears `super_read_only`.

```yaml
spec:
  mysql:
    clusterType: group-replication
    size: 3
    # replicationChannels removed: cluster is no longer a replica.
```

### 7.7 Reverse direction after promotion

The former replica is now writable. The former source can be re-enrolled as a replica by first ensuring its dataset is consistent with the new source (typically: stop writes on the new source, take a backup, restore into the former source's cluster, then resume) and then adding `replicationChannels` on the former source's CR pointing at the new source. If the former source had accepted writes that the new source has not, the operator detects GTID divergence and refuses to start replication; the user must re-bootstrap.

---

## 8. Error Handling and Edge Cases

### 8.1 All sources in a channel unreachable

**Scenario:** Every endpoint in `replicationChannels[].sources` is unreachable.

**Expected behavior:** MySQL's IO thread retries automatically per `config.sourceRetryCount` / `config.sourceConnectRetry`. The operator surfaces `channels[].state = Error` with `reason = SourceUnreachable` on the condition, and `lastError` populated from `Last_IO_Error`. No operator action beyond observation. When connectivity returns, MySQL resumes.

### 8.2 Authentication failure

**Scenario:** Password in `replicationUserSecretRef` does not match the `replication` user's password on the source (or the source's `replication` user has been dropped).

**Expected behavior:** `lastError` from MySQL surfaced verbatim. `reason = AuthenticationFailed`. Event `CrossSiteAuthenticationFailed`. When the referenced Secret is updated, the operator detects the change via watch and re-issues `STOP REPLICA; CHANGE REPLICATION SOURCE TO …; START REPLICA` for the affected channel.

### 8.3 TLS verification failure

**Scenario:** The CA in `config.caSecret` does not validate the source's cert (or `config.caSecret` is absent, `config.tlsSkipVerify` is `false`, and the source's cert is not system-trusted).

**Expected behavior:** `reason = TLSError`. Same recovery path as 8.2 — update the Secret, operator reconciles.

### 8.4 GTID divergence (including failed bootstrap)

**Scenario:** Replica's `gtid_executed` contains transactions whose `server_uuid`s are not in any source's `gtid_executed`, or the source has purged binlogs covering the gap. Causes include: the user set `replicationChannels` on a cluster that was never properly bootstrapped, the replica was promoted and accepted writes before being re-enrolled, or backups have aged out.

**Expected behavior:** MySQL's IO thread fails with error on `START REPLICA`. The operator observes `Last_IO_Error` and `Last_IO_Errno = 1236` on `SHOW REPLICA STATUS`, sets the channel to `state = Error`, `reason = GTIDDivergence`, copies the message verbatim into `lastError`. No automatic recovery; user must reset GTIDs manually or re-bootstrap.

### 8.5 In-replica primary failover

**Scenario:** Replica cluster's primary changes.

**Expected behavior:** Operator asserts every declared channel on the new primary and runs `RESET REPLICA ALL FOR CHANNEL` on the old primary. Once channels are running on the new primary, condition flips back to `Running`. The persisted channel config on the new primary lets MySQL resume from its saved GTID; no full re-bootstrap.

### 8.6 Channel add / remove / modify

**Scenario:** User edits `replicationChannels[]`.

**Expected behavior:** Reconciler diffs declared vs. currently configured channels on the primary. Adds: `CHANGE REPLICATION SOURCE TO … FOR CHANNEL '<new>'; START REPLICA FOR CHANNEL '<new>'`. Removes: `STOP REPLICA; RESET REPLICA ALL FOR CHANNEL '<old>'`. Renames are detected as remove-plus-add; the new channel restarts from the cluster's current GTID (works if source still has the binlogs, fails with divergence otherwise).

### 8.7 Source list updates within a channel

**Scenario:** User edits `replicationChannels[].sources` (add, remove, change weight).

**Expected behavior:** Reconciler diffs the rows in `mysql.replication_asynchronous_connection_failover` for the channel and converges via `_add_source` / `_delete_source` UDFs. No `CHANGE REPLICATION SOURCE` reissue. The failover list is hot-reloadable.

### 8.8 Replication user password rotation

**Scenario:** Source-side operator rotates the `replication` user's password.

**Expected behavior:** User updates the replica's Secret to match. Operator detects via watch and re-issues `CHANGE REPLICATION SOURCE TO … SOURCE_PASSWORD=<new>; START REPLICA`. If the user forgets to sync, 8.2 fires.

### 8.9 Source cluster scaling (async clusterType)

**Scenario:** Pods added/removed from an async-clusterType source cluster.

**Expected behavior:** Operator does not auto-discover. User updates each replica's `replicationChannels[].sources` manually. (For GR sources, `_add_managed()` handles this automatically)

### 8.10 Prevented configurations (validation-level)

The following are rejected at admission, not handled at runtime:

- Duplicate, empty, or invalid channel names.
- Empty `replicationChannels[].sources`.
- `weight` outside `[0, 100]`.
- `config.tls: false` without `unsafeFlags.crossSiteReplicationTLS: true`.

---

## 9. Migration and Backward Compatibility

### 9.1 Existing clusters

`replicationChannels` is absent by default. Existing CRs require no change. Operator upgrade onto an existing cluster is a no-op for cross-site purposes.

### 9.2 CRD compatibility

Changes are additive:

- New optional field `replicationChannels` under `spec.mysql`.
- New optional key in `UnsafeFlags` (`crossSiteReplicationTLS`).
- New optional top-level status block (`replicationChannels`).
- New condition type (`CrossSiteReplicating`).
- A new `replication` MySQL system user is added to the operator's known user list. Creation is gated on **`spec.crVersion`**: clusters at the new `crVersion` get the user (with a freshly-generated password written into their existing `spec.secretsName` Secret under the `replication` key); clusters at older `crVersion` values are untouched — no Secret diff, no MySQL user-management SQL, no rolling restart on operator upgrade. The user has only `REPLICATION SLAVE` and `REPLICATION CLIENT` grants. No DML or DDL, so its presence on clusters that do not act as cross-site sources is harmless. Adopting cross-site replication on an existing cluster therefore requires bumping `spec.crVersion` (the standard opt-in path for new features).

### 9.3 Operator version skew

- **Old operator, new CR:** Unknown fields are preserved by controller-runtime but not acted upon. No replication runs; behavior is identical to today.
- **New operator, old CR:** `replicationChannels` is absent → no cross-site behavior. Identical to today.

---

## 10. Testing Strategy

### 10.1 E2E test scenarios


| #   | Scenario                                      | Cluster types | What it validates                                                                                                                                                          |
| --- | --------------------------------------------- | ------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | Happy-path replication after manual bootstrap | GR, async     | Test fixture bootstraps the replica via Backup+Restore, then sets `replicationChannels`. Channels reach `Running`; writes on source land on replica; `super_read_only=ON`. |
| 2   | Multi-source consolidation                    | GR            | Two source clusters, one replica with two channels (non-overlapping schemas). Writes from each source land in the corresponding schema.                                    |
| 3   | Source primary failover                       | GR            | Force GR primary change on source. MySQL native auto-failover follows; `currentSource` status updates.                                                                     |
| 4   | Replica primary failover                      | GR            | Force GR primary change on replica. Channels move to new primary; old primary loses channels via `RESET REPLICA ALL`.                                                      |
| 5   | Promotion                                     | GR, async     | Remove `replicationChannels`; assert teardown, `super_read_only=OFF`, writes succeed.                                                                                      |
| 6   | Reverse direction with clean GTID             | GR            | After promotion, re-bootstrap former source via Backup+Restore, then add `replicationChannels` pointing at new source. Replication resumes.                                |
| 7   | GTID divergence detection                     | GR            | Set `replicationChannels` on a cluster whose `gtid_executed` is not a subset of the source's. MySQL IO thread fails with error 1236; operator surfaces `GTIDDivergence`.   |
| 8   | TLS-encrypted replication                     | GR            | Default config (TLS on). Verify `Replica_SSL_Allowed = Yes`.                                                                                                               |
| 9   | Fan-out (1 source → 2 replicas)               | GR            | Two replica clusters from one source; independent bootstrap and state.                                                                                                     |
| 10  | Adding a channel mid-flight                   | GR            | Patch CR to add a second channel; existing channel undisturbed.                                                                                                            |
| 11  | Removing a channel mid-flight                 | GR            | Patch CR to drop a channel; remaining channels untouched.                                                                                                                  |
| 12  | Secret rotation                               | GR            | Update `replicationUserSecretRef` Secret value. Operator re-applies `CHANGE REPLICATION SOURCE`.                                                                           |
| 13  | Source unreachable then recovers              | GR            | NetworkPolicy block; assert `SourceUnreachable`. Remove policy; assert recovery without operator action.                                                                   |
| 14  | Operator restart during replication           | GR            | Restart operator pod; replication continues; operator status converges.                                                                                                    |
| 15  | CRD validation rejects bad configs            | (no cluster)  | Submit each invalid spec from 8.12; assert rejection message.                                                                                                              |

---

## Appendix

### A. Glossary


| Term                             | Definition                                                                                                  |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| GR                               | Group Replication — MySQL's replication protocol.                                                           |
| Async clusterType                | Operator clusterType where one MySQL pod is primary and others are async replicas, managed by Orchestrator. |
| Cross-site replication           | Async MySQL replication between two clusters.                                                               |
| GTID                             | Global Transaction Identifier; `<server_uuid>:<transaction_id>`.                                            |
| Channel                          | A named, independent replication stream on a MySQL replica.                                                 |
| Asynchronous connection failover | MySQL 8.0.22+ feature where the IO thread auto-fails-over between a list of weighted sources.               |
| `super_read_only`                | MySQL flag preventing writes from all clients, including `SUPER`-privileged users.                          |
| PXB                              | Percona XtraBackup.                                                                                         |
| PiTR                             | Point-in-Time Recovery via binary log replay.                                                               |


### B. References

- [MySQL: Asynchronous Connection Failover for Sources](https://dev.mysql.com/doc/refman/8.0/en/replication-asynchronous-connection-failover-source.html)
- [MySQL: Asynchronous Connection Failover for Managed Sources](https://dev.mysql.com/doc/refman/8.0/en/replication-asynchronous-connection-failover-managed.html)
- [MySQL: CHANGE REPLICATION SOURCE TO Statement](https://dev.mysql.com/doc/refman/8.0/en/change-replication-source-to.html)
- [MySQL: Replication with Global Transaction Identifiers](https://dev.mysql.com/doc/refman/8.0/en/replication-gtids.html)
- [Percona Operator for MySQL: docs](https://docs.percona.com/percona-operator-for-mysql/ps/index.html)
- Incremental backup enhancement (similar template usage): `docs/enhancements/incremental-backups/incremental-backups.md`

