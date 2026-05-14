# [K8SPS-508]: Cross-Site Replication (GR clusters only)


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
- Supports `clusterType: group-replication` only in this initial release. Async-clusterType support is deferred (see Non-Goals).
- Each replica CR declares one or more named replication channels; each channel can list multiple source endpoints (used as seeds for GR membership discovery).
- MySQL-native source connection failover via `SOURCE_CONNECTION_AUTO_FAILOVER = 1`. For GR sources, `asynchronous_connection_failover_add_managed()` tracks membership changes inside the source cluster automatically.
- Replica cluster enforces `super_read_only = ON` on its primary while replicating.
- Promotion is user-driven by editing the CR (removing the channels). No automatic cross-site failover.
- TLS for the replication channel is on by default; disabling it requires an explicit `unsafeFlags` opt-in.

### 1.2 Non-Goals (Out of Scope)

- **Support for** `clusterType: async`. The async clusterType uses MySQL's standard async replication for in-cluster topology (Orchestrator-managed primary, IO/SQL threads on each replica). Adding cross-site on top introduces several non-trivial integration points. These are worth tackling in a follow-up once GR cross-site is stable.
- **Operator-managed initial seeding.** The user is responsible for bootstrapping the replica cluster (typically via a `PerconaServerMySQLRestore` from a backup of the source) *before* setting `replicationChannels`.
- **Bidirectional / active-active replication** — significant conflict resolution work; revisit later if needed.
- **Chained replication** (A → B → C) — every additional hop multiplies the failure modes; out of initial scope.
- **Automatic cross-site failover** — split-brain risk under WAN partitions is too high for an initial release. Manual promotion only.
- **Replication filters in the CRD** (`replicate-do-db`, `replicate-ignore-`*) — multi-source replication works correctly when sources have non-overlapping schemas; we document that constraint rather than exposing per-channel filters in v1.

---

## 2. Background

### 2.1 Core Concepts

**MySQL asynchronous replication.** A replica server's IO thread reads binary log events from a source over a TCP connection, persists them to a relay log, and a SQL thread applies them locally.

**Global Transaction Identifiers (GTID).** Every transaction is tagged with `<server_uuid>:<transaction_id>`. `server_uuid` is auto-generated and unique per MySQL instance. GTID-based replication (`SOURCE_AUTO_POSITION = 1`) removes the need for the operator to track binlog file/position; the replica asks for "everything I haven't applied yet."

**Multi-source replication.** A replica can have multiple *channels*, each with its own IO thread, SQL thread, GTID state, source endpoints, and failover config. Each channel is keyed by `channel_name` in the performance/replication tables.

**Native asynchronous connection failover** (MySQL 8.0.22+). When a channel is created with `SOURCE_CONNECTION_AUTO_FAILOVER = 1`, the replica's IO thread uses the `mysql.replication_asynchronous_connection_failover_managed` table (populated via `asynchronous_connection_failover_add_managed()`) to discover the source GR group's current membership and pick a member to read from. Each call to `_add_managed()` records one seed endpoint for the same managed group; MySQL needs only one of the seeds to be reachable at first connect to bootstrap discovery, after which it tracks members dynamically from the source's `performance_schema.replication_group_members`. Source-side primary changes and member additions/removals propagate without operator involvement.

**super_read_only.** A server-level flag that prevents writes from all clients, including users with `SUPER` privilege. The cross-site reconciler sets `super_read_only = ON` on the replica's primary while replicating, and clears it when channels are removed (promotion).

### 2.2 Key Constraints

1. **MySQL 8.0.22 or later.** Required for `SOURCE_CONNECTION_AUTO_FAILOVER` and the failover-table UDFs. Both supported MySQL versions (8.0, 8.4) include these features.
2. **Reachability between clusters.** The specified seed endpoint must be reachable via the replica cluster. The operator does not solve cross-cluster networking, that is the user's responsibility (LB hostnames, firewall rules, ServiceMesh, VPN).
3. **The user is responsible for the initial seed.** Before setting `spec.mysql.replicationChannels`, the user must ensure the replica cluster contains a consistent snapshot of the source's data. This is done typically by:
  1. Taking a `PerconaServerMySQLBackup` of the source.
  2. Restoring it into the replica cluster via `PerconaServerMySQLRestore` (using `backupSource` if the replica is in a different Kubernetes cluster).
  3. Verifying the restore is complete.
  4. Only then setting `replicationChannels` on the replica CR.
4. **The existing** `replication` **MySQL system user is reused.** The operator already provisions this user on every cluster as part of the standard system-users set, with grants that comfortably cover both the IO thread (`REPLICATION SLAVE`, `REPLICATION CLIENT`) and the operator-side discovery probe (`SELECT` on `*.`*, `SYSTEM_VARIABLES_ADMIN`). Its password lives under the `replication` key in `spec.secretsName`. The replica admin reads this password from the source cluster's Secret, places it into a Secret in the replica's namespace, and references that via `replicationUserSecretRef`. No new user, no new grants, no user-management code change. If the replica cluster happens to be set up with the same `replication` password as the source, `replicationUserSecretRef` may be skipped.
5. **Backward compatibility.** Existing CRs (without `replicationChannels`) must continue to work as usual (no changes).

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
- a `replication` key in its `spec.secretsName` Secret (the existing user-management code provisions the MySQL user).

The replica cluster reconciler grows a new phase that activates when `spec.mysql.replicationChannels` is non-empty. The flow is broken into four phases, ordered carefully: GR broadcasts the `SOURCE_CONNECTION_AUTO_FAILOVER` state and the source list to all group members when a primary enables them, so the channel and filter must already exist on every member before that broadcast fires.

```
ps reconciler (for each declared channel)
  ├─ phase A: per-member channel + filter setup (on EVERY GR member)
  │    On each pod, idempotently:
  │      CHANGE REPLICATION SOURCE TO
  │        SOURCE_HOST=<first reachable channel.sources entry>,
  │        SOURCE_PORT=<...>,
  │        SOURCE_USER='replication',
  │        SOURCE_PASSWORD=<from replicationUserSecretRef>,
  │        SOURCE_AUTO_POSITION=1,
  │        SOURCE_SSL=<config.tls ? 1 : 0>,
  │        SOURCE_SSL_VERIFY_SERVER_CERT=<config.tlsSkipVerify ? 0 : 1>,
  │        SOURCE_SSL_CA=<from config.caSecret>,
  │        SOURCE_RETRY_COUNT=<config.sourceRetryCount>,
  │        SOURCE_CONNECT_RETRY=<config.sourceConnectRetry>
  │        FOR CHANNEL '<channel.name>';
  │      -- IMPORTANT: do not set SOURCE_CONNECTION_AUTO_FAILOVER here.
  │
  │      CHANGE REPLICATION FILTER
  │        REPLICATE_WILD_IGNORE_TABLE = (
  │          'mysql.%',
  │          'mysql_innodb_cluster_metadata.%'
  │        )
  │        FOR CHANNEL '<channel.name>';
  │
  ├─ phase B: failover-managed table population (on the current primary)
  │    Discover the source's GR group UUID:
  │      probe one reachable endpoint in `channel.sources` using the
  │      `replication` user; SELECT @@global.group_replication_group_name.
  │      If empty/non-GR: set state = Error, reason = UnsupportedSource;
  │        skip Phase C-D for this channel.
  │
  │    For each entry in `channel.sources`:
  │      SELECT asynchronous_connection_failover_add_managed(
  │        '<channel.name>', 'GroupReplication', <discovered group UUID>,
  │        <host>, <port>, '', 80, 60
  │      );
  │    -- rows replicate to secondaries via GR; secondaries already
  │    -- have the channel locally (from Phase A), so the row is consistent.
  │
  ├─ phase C: activate AUTO_FAILOVER (on the current primary)
  │    CHANGE REPLICATION SOURCE TO
  │      SOURCE_CONNECTION_AUTO_FAILOVER=1
  │      FOR CHANNEL '<channel.name>';
  │    -- this is the statement that triggers GR's broadcast of AUTO_FAILOVER
  │    -- state and the source list to all GR members. Because Phase A is
  │    -- already done on every member, the broadcast applies cleanly.
  │
  └─ phase D: start replication + fence writes (on the current primary)
       START REPLICA FOR CHANNEL '<channel.name>';
       SET GLOBAL super_read_only = ON;
       -- Note: SET GLOBAL, not SET PERSIST. We do not want this baked
       -- into mysqld-auto.cnf — only the current GR primary is fenced;
       -- on primary failover, GR clears super_read_only on the new primary,
       -- and the operator re-asserts it on the next reconcile.

Drift reconciliation (every loop):
  - Confirm channel + filter present on every member (re-apply Phase A on
    anything that drifted, e.g., a pod with a freshly-restored datadir
    that hasn't been touched yet).
  - Re-assert SET GLOBAL super_read_only = ON on the current primary.
  - Surface SHOW REPLICA STATUS into status.

In-replica primary failover:
  GR's mysql_start_failover_channels_if_primary member action starts the
  channel on the newly-elected primary (documented). The demoted ex-primary
  side is not explicitly documented; the operator defensively runs
  STOP REPLICA FOR CHANNEL '<name>' on any non-primary member where it
  observes the channel still running. The operator also re-asserts
  SET GLOBAL super_read_only = ON on the current primary.

Teardown (when replicationChannels is removed):
  On every member:
    STOP REPLICA FOR CHANNEL '<name>';
    RESET REPLICA ALL FOR CHANNEL '<name>';   -- also clears the filter
  On the current primary:
    SELECT asynchronous_connection_failover_delete_managed(
      '<name>', '<source group UUID from status>'
    );
    SET GLOBAL super_read_only = OFF;
```

### 3.3 Key Observations

1. **The channel is configured on every GR member, but only runs on the current primary.** Phase A creates the channel config and the replication filter on every pod. GR's `mysql_start_failover_channels_if_primary` member action ensures the channel's IO/SQL threads run only on whichever pod is the GR primary at any moment. Other pods hold the channel in a stopped state, ready to take over.
2. **All cross-site state is per-cluster and recoverable from MySQL.** Channel config, filters, and the managed-failover row all live in MySQL system tables that survive pod restarts and propagate to new members via GR clone-based recovery.
3. **The source side has no cross-site-specific reconciler logic.** A source is a cluster with exposed pods and a `replication` user; nothing in the CR explicitly marks it as a source.
4. **MySQL's native auto-failover replaces operator-side source primary discovery.** The replica reconciler never polls source endpoints to find a writable primary; `_add_managed()` plus MySQL's IO thread do that.
5. **Bootstrap is user-driven.** The reconciler never invokes restore code paths. If the user mis-bootstraps the replica, MySQL itself rejects the IO thread on `START REPLICA` with a GTID-divergence error (typically MySQL error 1236); the operator surfaces that error verbatim. There is no operator-side pre-flight check.
6. **GR's `mysql_start_failover_channels_if_primary` member action starts the channel on the newly-elected primary.** Per [MySQL 8.4: Asynchronous Connection Failover for Replicas](https://dev.mysql.com/doc/refman/8.4/en/replication-asynchronous-connection-failover-replica.html), *"the new primary starts replication on the same channel when it is elected."* The docs do not explicitly say whether the demoted ex-primary stops its channel automatically. It should, since otherwise two members would be IO-threading from the source simultaneously, but the operator should verify on each reconcile and run `STOP REPLICA FOR CHANNEL '<name>'` on any non-primary member found running the channel as a defensive measure. Separately, the operator re-asserts `SET GLOBAL super_read_only = ON` on the current primary, since GR clears `super_read_only` on the primary as part of single-primary-mode election ([GR Primary Elections](https://dev.mysql.com/doc/refman/8.4/en/group-replication-primary-elections.html)).
7. **Scale-up relies on GR's CLONE-based distributed recovery.** A new member joining the replica cluster receives a copy of an existing member's datadir, which includes `mysql.slave_master_info` (channel config), `mysql.replication_applier_filters_configuration` (filter), and the `_add_managed()` row. No per-pod fanout from the operator is needed on scale-up. In order for this to work, any new node on the replica custer must join via CLONE.

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
          name: source-replication-creds    # Optional Secret containing the replication password.
          key: replication                  
        sources:
          - host: cluster1-mysql-0.example.com
            port: 3306
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
            key: ca.cr
```

#### 4.1.1 Field semantics

- `**replicationChannels**` *(list, optional, default empty)*. Non-empty list declares the cluster a replica. The operator will reconcile cross-site channels on whichever pod is currently the cluster's primary. Maximum 16 channels (CEL-validated; practical ceiling tied to applier worker defaults; can be raised later).
- `**replicationChannels[].name`** *(string, required)*. MySQL channel name. Must match `[a-zA-Z0-9_]+`, length 1–64. Must be unique within the list. Renaming a channel is observed by the reconciler as remove-old + add-new.
- `**replicationChannels[].replicationUserSecretRef`** *(SecretKeySelector, optional)*. References a Secret in the same namespace as the replica CR. Reads the password for the `replication` MySQL user from the named key (default `password`). The IO thread always authenticates as `replication` there is no field to override the username. The Secret is watched; updates trigger a `CHANGE REPLICATION SOURCE TO ... SOURCE_PASSWORD=<new>` reissue for the affected channel. May be omitted when the replica's own `spec.secretsName` already holds the same password as the source under the `replication` key, in which case the operator authenticates with the local secret.
- `**replicationChannels[].sources`** *(list, required, non-empty)*. Seed endpoints for GR membership discovery. Each entry is recorded as a row in `mysql.replication_asynchronous_connection_failover_managed` via `asynchronous_connection_failover_add_managed()`; MySQL only needs *one* of them to be reachable at first connect to bootstrap discovery of the source's GR group, after which it tracks members dynamically from the source's `performance_schema.replication_group_members`. Multiple entries give redundancy against any particular seed pod being unreachable.
- `**replicationChannels[].sources[].host`** / `**.port*`* *(string / int, required)*. Externally reachable host:port of a source-cluster pod. Typically derived from the source's per-pod LoadBalancer/NodePort hostnames.
- `**replicationChannels[].config`** *(object, optional)*. Per-channel knobs for retry behavior and TLS.
- `**replicationChannels[].config.sourceRetryCount`** / `**.sourceConnectRetry*`* *(uint32)*. Forwarded into `CHANGE REPLICATION SOURCE TO`.
- `**replicationChannels[].config.tls`** *(bool, default `true`)*. Sets `SOURCE_SSL = 1` on the channel when true. Disabling requires `unsafeFlags.crossSiteReplicationTLS`.
- `**replicationChannels[].config.tlsSkipVerify`** *(bool, default `false`)*. When true, sets `SOURCE_SSL_VERIFY_SERVER_CERT = 0` — the IO thread encrypts the connection but does not validate the source's certificate. Useful for self-signed certs in lab/test environments. Ignored when `config.tls` is `false`.
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
3. Any `replicationChannels[].config.tls: false` requires `unsafeFlags.crossSiteReplicationTLS: true`.
4. `len(replicationChannels) ≤ 16`.
5. `replicationChannels` non-empty is mutually exclusive with `pause: true`.
6. `replicationChannels` non-empty requires `spec.mysql.clusterType: group-replication`. Async-clusterType clusters reject the field at admission for this release.

### 4.2 CRD Status Changes

```yaml
status:
  replicationChannels:                    # This is set only on replicating clusters.
    replicatingPod: cluster2-mysql-0      # pod currently running the
                                          # cross-site channels.
    channels:
      - name: ch_primary
        state: Running | Stopped | Error
        sourceGroupName: aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee
                                          # auto-discovered by the operator
                                          # via SELECT @@global.group_replication_group_name
                                          # on a reachable source endpoint. Surfaced
                                          # read-only for verification / audit.
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
        | UnsupportedSource
        | Stopped
  message: <details>
```

Aggregated: `True` only if every channel is `Running`.

### 4.3 Internal Contracts

- **Reuse of the existing `replication` system user.** The operator already provisions this user on every cluster as part of the standard system-users set. Its existing grants cover both the IO thread (`REPLICATION SLAVE`, `REPLICATION CLIENT`) and the operator-side probe (`SELECT` on `*.`*, `SYSTEM_VARIABLES_ADMIN`).
- **MySQL config preconditions for the cross-site path.** `gtid_mode = ON` and `enforce_gtid_consistency = ON` are already set by `build/ps-entrypoint.sh` for every operator-managed cluster. `server_id` is derived from a cluster-hash prefix in the same entrypoint, so cross-cluster collisions are not a concern. GR's own protocol requirements (binary logging, replicated-write emission) are taken as given on `clusterType: group-replication` clusters; no additional assertion is made by the cross-site reconciler.
- **Per-channel replication filter managed by the operator.** Before activating `SOURCE_CONNECTION_AUTO_FAILOVER` on a channel, the operator runs:
  ```sql
  CHANGE REPLICATION FILTER
    REPLICATE_WILD_IGNORE_TABLE = ('mysql.%', 'mysql_innodb_cluster_metadata.%')
    FOR CHANNEL '<name>';
  ```
  on every GR member. This filter suppresses row events on (a) the MySQL system schema (`mysql.user` updates from mysqlsh recovery-account management, etc.) and (b) the InnoDB Cluster metadata schema (per-cluster topology state that doesn't apply across clusters). Without these filters, the cross-site channel hits applier errors during normal source-side mysqlsh activity (cluster bootstrap, member add/remove, primary switchover, recovery account password rotation). The filter is not user-configurable.  
  **Trade-off:** the `mysql.%` portion of the filter also blocks legitimate replication of any user-issued changes to the `mysql` schema — including `CREATE USER`, `GRANT`, `mysql.user` updates for application users. Users who want to sync application user accounts between source and replica clusters must do so out of band.
- **GR's** `mysql_start_failover_channels_if_primary` **member action starts the channel on the newly-elected primary.** Documented in [MySQL 8.4: Asynchronous Connection Failover for Replicas](https://dev.mysql.com/doc/refman/8.4/en/replication-asynchronous-connection-failover-replica.html): *"the new primary starts replication on the same channel when it is elected."* The docs do not explicitly cover the demoted ex-primary side; the operator therefore defensively runs `STOP REPLICA FOR CHANNEL '<name>'` on any non-primary member where it finds the channel running. Separately, the operator re-asserts `SET GLOBAL super_read_only = ON` on the current primary on every reconcile, because GR clears `super_read_only` on the elected primary as part of single-primary-mode election.
- **Scale-up via CLONE-based recovery.** The operator must always invoke `dba.addInstance(..., {recoveryMethod: 'clone'})` for new members on the replica cluster. CLONE copies the donor's data, which includes `mysql.slave_master_info` (channel config), `mysql.replication_applier_filters_configuration` (filter), and `mysql.replication_asynchronous_connection_failover_managed` (managed-source rows). A new pod therefore comes up with everything in place; the operator does not run Phase A on it explicitly. This relies on CLONE staying the default recovery method for members on a replica cluster.
- `**mysql.replication_asynchronous_connection_failover_managed` is managed by the operator.** Reconciler diffs declared sources against the table contents per channel and converges via `_add_managed` / `_delete_managed` UDFs. Each row uses the same discovered group UUID; multiple rows represent multiple seed endpoints into the same group. Users editing the table directly may have their edits reverted on the next reconcile.

### 4.4 User-Facing Behavior Changes

- New events emitted: `CrossSiteReplicating`, `CrossSiteSourceUnreachable`, `CrossSiteAuthenticationFailed`, `CrossSiteTLSError`, `CrossSiteGTIDDivergence`, `CrossSiteReplicatingPodChanged`, `CrossSitePromoted`.
- No webhook/admission changes beyond the CEL rules above.

---

## 5. Design Decisions and Alternatives

### 5.1 Fields on the existing CR, under `spec.mysql`

**Chosen approach:** Add `replicationChannels` directly under `spec.mysql`, alongside existing per-cluster MySQL knobs.

**Why:** Configuration is per-cluster and works across separate Kubernetes clusters without cross-cluster CR references. Reuses existing primitives (per-pod expose, system-user provisioning). The placement under `spec.mysql` mirrors how other MySQL-scoped settings (`expose`, `clusterType`, `configuration`) live.

**Alternatives considered:**


| Alternative                                                                                    | Why Rejected                                                                                                                              |
| ---------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| New top-level block (`spec.crossSiteReplication`) with an `enabled` flag and a `channels` list | Adds an `enabled` master switch that is redundant, presence of channels already implies activation.                                       |
| New CRD: `PerconaServerMySQLReplication` referencing two clusters by name                      | Cross-cluster references don't actually work across separate K8s clusters without federation.                                             |
| Reuse `clusterType: async`, no new spec                                                        | Conflates in-cluster async HA with inter-cluster replication. Orchestrator was not designed for external sources. Hostile to the GR path. |


### 5.2 Channel-shaped CRD with multi-source support

**Chosen approach:** `spec.mysql.replicationChannels` is a list from day one, and the controller supports multi-source replication.

**Why:** PXC operator parity. Users moving between Percona operators see a consistent shape. Multi-source consolidation is a real use case. The CRD shape directly mirrors MySQL's multi-source feature.

**Alternatives considered:**


| Alternative                               | Why Rejected                                                                                                                                            |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Flat `sources` field, single channel only | Simpler today, but a future multi-source need requires either a breaking change or a parallel field with mutual-exclusion validation. Loses PXC parity. |


### 5.3 MySQL-native asynchronous connection failover, not operator polling

**Chosen approach:** `CHANGE REPLICATION SOURCE … SOURCE_CONNECTION_AUTO_FAILOVER = 1` plus the failover table. For GR sources, use `asynchronous_connection_failover_add_managed()` so MySQL tracks the source cluster's membership directly.

**Why:** Source failover happens at MySQL's IO-thread level, not at operator-reconcile latency. No operator polling code, no `CHANGE REPLICATION SOURCE` re-issue on every in-source primary change.

**Alternatives considered:**


| Alternative                                                                                          | Why Rejected                                                                                                                                       |
| ---------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| Operator polls each endpoint to find writable primary, re-issues CHANGE REPLICATION SOURCE on change | Slower failover. More code. Worse availability semantics. Reinvents a MySQL-native feature.                                                        |
| Static `SOURCE_HOST` to a Service that points at the primary                                         | Service-update lag during in-source failover. Requires the operator to keep the Service endpoints in sync, same coordination cost, fewer benefits. |


### 5.4 Source endpoints are seeds, not a writers list; no per-endpoint weight

**Chosen approach:** Users list one or more source-cluster pod endpoints in `replicationChannels[].sources`. Each entry is passed to `asynchronous_connection_failover_add_managed()` as a seed for the source's GR group. MySQL only needs *one* of the seeds to be reachable to bootstrap discovery; after that, it tracks members and the current primary via the source's `performance_schema.replication_group_members`. No per-endpoint weight is exposed.

**Why:** For a managed GR source, the failover UDF takes role-based weights (primary vs. secondary), not per-endpoint weights — so a `weight: N` field on each `sources[]` entry doesn't map cleanly to MySQL's model. Exposing it would either be silently ignored or require collapsing user intent into role weights in a way users wouldn't predict. Multiple endpoints are still useful as seeds, they protect against a specific pod/network being unreachable at first connect. PXC operator's per-endpoint weights make sense because PXC is multi-writer Galera, where every node is a peer; that semantic doesn't transfer to GR single-primary.

**Alternatives considered:**


| Alternative                            | Why Rejected                                                                                                                                                                                                                           |
| -------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Keep per-endpoint `weight` (PXC-style) | `_add_managed()` does not consume per-endpoint weights; the field would be silently ignored or require an unintuitive mapping to role weights. Can be reintroduced additively if/when async support lands and `_add_source()` is used. |


### 5.5 Manual, user-driven promotion

**Chosen approach:** Promotion happens by removing `replicationChannels` from the replica CR. The operator runs `STOP REPLICA; RESET REPLICA ALL` and clears `super_read_only`.

**Why:** Automating this can be pretty complex for an initial implementation.

**Alternatives considered:**


| Alternative                                                             | Why Rejected                                                                                                            |
| ----------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| Operator-orchestrated promotion API (`role: source` or `role: replica`) | Adds an imperative-feeling field a declarative CRD. Same outcome as editing `replicationChannels` . Not enough benefit. |
| Automatic failover on source-unreachable detection                      | Too dangerous Out of scope.                                                                                             |


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


### 5.7 Two-step `CHANGE REPLICATION SOURCE TO` with channel-on-every-member

**Chosen approach:** Configure the channel on every GR member of the replica cluster *without* `SOURCE_CONNECTION_AUTO_FAILOVER` first, then enable AUTO_FAILOVER in a second `CHANGE REPLICATION SOURCE TO` on the current primary only.

**Why:** GR actively broadcasts the AUTO_FAILOVER state and the source list to all group members. When a primary enables `SOURCE_CONNECTION_AUTO_FAILOVER=1`, the broadcast tries to apply that setting to the named channel on every member. Members without the channel pre-configured fail this broadcast with MY-013786 ("Unable to set SOURCE_CONNECTION_AUTO_FAILOVER on a non-existent or misconfigured replication channel") and leave the GR group. The two-step pattern ensures every member has the channel locally before the broadcast fires.

Documented by MySQL: ["Asynchronous Connection Failover for Replicas"](https://dev.mysql.com/doc/refman/8.4/en/replication-asynchronous-connection-failover-replica.html) — *"The replication channel and the replication user account and password for the channel must be set up on all the member servers in the replication group ... The SOURCE_CONNECTION_AUTO_FAILOVER setting for the channel is broadcast to group members from the primary when they join."*

**Alternatives considered:**


| Alternative                                                                                 | Why Rejected                                                                                                                                                                                                                                                                      |
| ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Single `CHANGE REPLICATION SOURCE TO ... SOURCE_CONNECTION_AUTO_FAILOVER=1` on primary only | Verified, and fails with MY-013786 on every replica-cluster secondary as soon as the broadcast fires. Channel configuration is not binlogged so it doesn't propagate via GR; only the broadcast does, which requires the local channel to exist on secondaries.                   |
| Use `_add_source()` instead of `_add_managed()` and skip AUTO_FAILOVER on the primary       | Loses source-side GR membership tracking. Replica then has to be reconfigured by the operator each time the source cluster scales, and source-primary changes inside the source cluster become invisible to MySQL's IO thread, requiring operator-side polling or manual changes. |


### 5.8 No source-side CR fields

**Chosen approach:** A source cluster has no cross-site-specific CR fields. It is a normal cluster with `spec.mysql.expose` configured and a `replication` key in its `spec.secretsName` Secret.

**Why:** The operator only needs cross-site logic on the replica side, since MySQL's native failover takes care of source-side primary tracking. Adding source-side fields would require either symmetric declarations (every source CR carries a flag) or replicas-list coupling (source enumerates its replicas) — both reintroduce coordination cost without delivering capability.

**Alternatives considered:**


| Alternative                                                                | Why Rejected                                            |
| -------------------------------------------------------------------------- | ------------------------------------------------------- |
| `spec.mysql.crossSiteSource: true` flag on the source CR                   | Pure declaration with no functional effect; UX clutter. |
| `replicas: [host:port, ...]` field on the source listing expected replicas | Requires cross-cluster knowledge on the source side     |


### 5.9 No user-configurable replication filters; operator-managed internal filters only

**Chosen approach:** The CRD exposes no fields for replication filters. The operator applies one fixed filter on every cross-site channel:

```sql
CHANGE REPLICATION FILTER
  REPLICATE_WILD_IGNORE_TABLE = ('mysql.%', 'mysql_innodb_cluster_metadata.%')
  FOR CHANNEL '<channel.name>';
```

**Why:** Testing showed that without this filter, the cross-site channel hits applier errors during normal source-side operation:

- Row events on `mysql_innodb_cluster_metadata.`* (cluster topology metadata that mysqlsh maintains per-cluster) fail with `HA_ERR_KEY_NOT_FOUND` because the replica cluster's metadata tables describe its own members, not the source's.
- Row events on `mysql.user` and adjacent system tables fail when mysqlsh's `dba` API operations issue user-management SQL against `mysql_innodb_cluster_<server_id>` recovery accounts that exist on the source but not the replica (different server_ids → different account names).

The filter is operator-managed and not user-facing because it's a correctness requirement, not a preference — without it, the feature does not work between two PSMO GR clusters.

**Trade-off, documented for users:** the `mysql.%` portion of the filter also suppresses legitimate replication of user-issued `CREATE USER` / `GRANT` / `mysql.user` changes for application accounts. Users wanting application user/grant state synced across clusters must do so out of band. This is the cost of cross-site replication between two mysqlsh-managed GR clusters; it is not a limitation we can paper over from the operator side.

**Alternatives considered:**


| Alternative                                                                                                                   | Why Rejected                                                                                                                                                                                                                                                                                                                                                                                                                                                     |
| ----------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Expose filter rules as user-configurable CRD fields                                                                           | Adds CR surface for what is fundamentally an operator-correctness concern. Users could disable the filter and break replication. Internal management is safer and matches PXC operator's approach.                                                                                                                                                                                                                                                               |
| Use `replica_skip_errors` to silently skip applier errors (1032, 1396, 1410)                                                  | Server-startup-only (requires restart) and blanket-skips error codes that might fire on legitimate user-data issues. The filter is narrower and per-channel.                                                                                                                                                                                                                                                                                                     |
| Operator-managed shell-user mirroring (replicate source's `mysql_innodb_cluster_`* users to replica)                          | Solves only the user-management side. Doesn't help with `mysql_innodb_cluster_metadata.`* row events. Combined with the filter, would be redundant. Filter alone is simpler and sufficient.                                                                                                                                                                                                                                                                      |
| Adopt MySQL InnoDB ClusterSet ([upstream MySQL feature](https://dev.mysql.com/doc/mysql-shell/8.4/en/innodb-clusterset.html)) | Less restrictive but more complex to integrate into the operator: replica clusters cannot be operator managed as it is today. They must be initialised via `ClusterSet.createReplicaCluster()` against a standalone instance, a different bootstrap path that doesn't go through normal `dba.createCluster()`, and distinct recovery/promotion logic wrapping `cs.setPrimaryCluster()` / `cs.forcePrimaryCluster()`. This potentially also requires its own CRD. |


---

## 6. Replication Model Impact

This release supports `clusterType: group-replication` only. Async clusterType shall be added later.

### 6.1 Source cluster (GR)

- Replica side uses `asynchronous_connection_failover_add_managed()` with the source-cluster endpoint(s) provided in `replicationChannels[].sources`; MySQL queries the source's `performance_schema.replication_group_members` to maintain the failover list dynamically.
- In-source primary failover is invisible to the operator.
- Source-cluster scaling is invisible to the operator — MySQL's failover list updates automatically from GR membership.

### 6.2 Replica cluster (GR)

- Channels run on the GR primary only. On a GR primary change, the operator re-asserts channels on the new primary and runs `RESET REPLICA ALL FOR CHANNEL '<name>'` on the old primary.
- `super_read_only = ON` is set cluster-wide via in-cluster GR replication — the operator sets it on the primary, GR propagates the change.

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
2. Read the auto-generated `replication` user's password from the cluster's `spec.secretsName` Secret (key `replication`) and copy it into a Secret in the replica's namespace (used later via `replicationUserSecretRef`). Alternatively, the replica cluster may be created with this password.
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
          - {host: source-mysql-0.example.com, port: 3306}
          - {host: source-mysql-1.example.com, port: 3306}
          - {host: source-mysql-2.example.com, port: 3306}
        config:
          tls: true
          caSecret:
            name: source-ca
            key: ca.crt
```

### 7.6 Promotion (replica → standalone)

Remove `replicationChannels` (or set it to an empty list). The operator runs `STOP REPLICA; RESET REPLICA ALL` and clears `super_read_only`.

```yaml
spec:
  mysql:
    clusterType: group-replication
    size: 3
    # replicationChannels removed: cluster is no longer a replica.
```

---

## 8. Error Handling and Edge Cases

### 8.1 All sources in a channel unreachable

**Scenario:** Every endpoint in `replicationChannels[].sources` is unreachable.

**Expected behavior:** MySQL's IO thread retries automatically per `config.sourceRetryCount` / `config.sourceConnectRetry`. The operator surfaces `channels[].state = Error` with `reason = SourceUnreachable` on the condition, and `lastError` populated from `Last_IO_Error`. No operator action beyond observation. When connectivity returns, MySQL resumes.

### 8.2 Authentication failure

**Scenario:** Password in `replicationUserSecretRef` does not match the `replication` user's password on the source (or the source's `replication` user has been dropped). Neither is the cluster created with the source'r `replication` password.

**Expected behavior:** `lastError` from MySQL surfaced verbatim. `reason = AuthenticationFailed`. Event `CrossSiteAuthenticationFailed`. When the referenced Secret is updated, the operator detects the change via watch and re-issues `STOP REPLICA; CHANGE REPLICATION SOURCE TO …; START REPLICA` for the affected channel.

### 8.3 TLS verification failure

**Scenario:** The CA in `config.caSecret` does not validate the source's cert (or `config.caSecret` is absent, `config.tlsSkipVerify` is `false`, and the source's cert is not system-trusted).

**Expected behavior:** `reason = TLSError`. Same recovery path as 8.2 — update the Secret, operator reconciles.

### 8.4 Source is not a Group Replication cluster

**Scenario:** Operator probes the source via `SELECT @@global.group_replication_group_name` and the result is empty or all-zero — the endpoint is not a GR cluster (e.g., a standalone MySQL, an async-clusterType cluster, a non-Percona MySQL).

**Expected behavior:** Channel is set to `state = Error`, `reason = UnsupportedSource`. `lastError` records "source endpoint [host:port](host:port) reports no Group Replication group; cross-site replication requires a group-replication source in this release." No retry beyond the next reconcile; user must point `sources[]` at a GR cluster or wait for async support.

### 8.5 GTID divergence (including failed bootstrap)

**Scenario:** Replica's `gtid_executed` contains transactions whose `server_uuid`s are not in any source's `gtid_executed`, or the source has purged binlogs covering the gap. Causes include: the user set `replicationChannels` on a cluster that was never properly bootstrapped, the replica was promoted and accepted writes before being re-enrolled, or backups have aged out.

**Expected behavior:** MySQL's IO thread fails with error on `START REPLICA`. The operator observes `Last_IO_Error` and `Last_IO_Errno = 1236` on `SHOW REPLICA STATUS`, sets the channel to `state = Error`, `reason = GTIDDivergence`, copies the message verbatim into `lastError`. No automatic recovery; user must reset GTIDs manually or re-bootstrap.

### 8.6 In-replica primary failover

**Scenario:** Replica cluster's primary changes.

**Expected behavior:** GR's `mysql_start_failover_channels_if_primary` member action [automatically starts the channel on the newly-elected primary](https://dev.mysql.com/doc/refman/8.4/en/replication-asynchronous-connection-failover-replica.html). The docs don't explicitly cover what happens on the demoted ex-primary, so the operator defensively runs `STOP REPLICA FOR CHANNEL '<name>'` on any non-primary member found with the channel running. The operator re-asserts `SET GLOBAL super_read_only = ON` on the current primary (since GR clears it on promotion). Channel state survives the role change because Phase A previously configured the channel on every member; `_add_managed` rows replicated to every member via GR DML; the filter is present on every member.

### 8.7 Channel add / remove / modify

**Scenario:** User edits `replicationChannels[]`.

**Expected behavior:** Reconciler diffs declared vs. currently configured channels.

- *Add:* run full Phase A through Phase D for the new channel: per-member CHANGE REPLICATION SOURCE TO + replication filter, then `_add_managed()` + AUTO_FAILOVER toggle + START REPLICA on the current primary.
- *Remove:* on every member, `STOP REPLICA FOR CHANNEL '<old>'; RESET REPLICA ALL FOR CHANNEL '<old>';` (this also clears the channel's filter). On the current primary, `asynchronous_connection_failover_delete_managed('<old>', '<group UUID>')`.

### 8.8 Source list updates within a channel

**Scenario:** User edits `replicationChannels[].sources` (add or remove a seed endpoint).

**Expected behavior:** Reconciler diffs the rows in `mysql.replication_asynchronous_connection_failover_managed` for the channel and converges via `_add_managed` / `_delete_managed` UDFs (one row per seed endpoint, all sharing the same discovered group UUID).

### 8.9 Replication user password rotation

**Scenario:** Source-side operator rotates the `replication` user's password.

**Expected behavior:** User updates the replica's Secret to match. Operator detects, re-issues `CHANGE REPLICATION SOURCE TO … SOURCE_PASSWORD=<new>; START REPLICA`.

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
- No new MySQL system users or grants are introduced. The cross-site feature reuses the existing `replication` user (see Section 4.3 and Constraint 4 in Section 2.2). Operator upgrade onto an existing cluster does not modify `spec.secretsName` for cross-site purposes and does not trigger a rolling restart on this account.

### 9.3 Operator version skew

- **Old operator, new CR:** Unknown fields are preserved by controller-runtime but not acted upon. No replication runs; behavior is identical to today.
- **New operator, old CR:** `replicationChannels` is absent → no cross-site behavior. Identical to today.

---

## 10. Testing Strategy

### 10.1 E2E test scenarios


| #   | Scenario                                      | Cluster types | What it validates                                                                                                                                                  |
| --- | --------------------------------------------- | ------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Happy-path replication after manual bootstrap | GR            | Test bootstraps the replica via Backup+Restore, then sets `replicationChannels`. Channels reach `Running`; writes on source land on replica; `super_read_only=ON`. |
| 2   | Source primary failover                       | GR            | Force GR primary change on source. MySQL native auto-failover follows; `currentSource` status updates.                                                             |
| 3   | Replica primary failover                      | GR            | Force GR primary change on replica. Channels move to new primary; old primary loses channels via `RESET REPLICA ALL`.                                              |
| 4   | Promotion                                     | GR            | Remove `replicationChannels`; assert teardown, `super_read_only=OFF`, writes succeed.                                                                              |
| 5   | Secret rotation                               | GR            | Update `replicationUserSecretRef` Secret value. Operator re-applies `CHANGE REPLICATION SOURCE`.                                                                   |


---

## Appendix

### A. Glossary


| Term                             | Definition                                                                                                                                                           |
| -------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| GR                               | Group Replication — MySQL's replication protocol.                                                                                                                    |
| Async clusterType                | Operator clusterType where one MySQL pod is primary and others are async replicas, managed by Orchestrator.                                                          |
| Cross-site replication           | Async MySQL replication between two clusters.                                                                                                                        |
| GTID                             | Global Transaction Identifier; `<server_uuid>:<transaction_id>`.                                                                                                     |
| Channel                          | A named, independent replication stream on a MySQL replica.                                                                                                          |
| Asynchronous connection failover | MySQL 8.0.22+ feature where the IO thread auto-fails-over between a list of source endpoints; for a managed GR source, it tracks the group's membership dynamically. |
| `super_read_only`                | MySQL flag preventing writes from all clients, including `SUPER`-privileged users.                                                                                   |
| PXB                              | Percona XtraBackup.                                                                                                                                                  |
| PiTR                             | Point-in-Time Recovery via binary log replay.                                                                                                                        |


### B. References

- [MySQL: Asynchronous Connection Failover for Sources](https://dev.mysql.com/doc/refman/8.0/en/replication-asynchronous-connection-failover-source.html)
- [MySQL: Asynchronous Connection Failover for Managed Sources](https://dev.mysql.com/doc/refman/8.0/en/replication-asynchronous-connection-failover-managed.html)
- [MySQL: CHANGE REPLICATION SOURCE TO Statement](https://dev.mysql.com/doc/refman/8.0/en/change-replication-source-to.html)
- [MySQL: Replication with Global Transaction Identifiers](https://dev.mysql.com/doc/refman/8.0/en/replication-gtids.html)
- [Percona Operator for MySQL: docs](https://docs.percona.com/percona-operator-for-mysql/ps/index.html)
- Incremental backup enhancement (similar template usage): `docs/enhancements/incremental-backups/incremental-backups.md`

