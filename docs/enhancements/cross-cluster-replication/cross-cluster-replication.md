# K8SPS-508: Cross-site replication using InnoDB ClusterSet


| Field        | Value       |
| ------------ | ----------- |
| Author       | Mayank Shah |
| Status       | Draft       |
| Created      | 2026-05-18  |
| Last Updated | 2026-05-25  |
| Reviewers    |             |


---

## 1. Overview

This feature adds support for cross-cluster replication capabilities between GR (group replication) clusters. A new namespaced CRD `PerconaServerMySQLClusterSet` (or `ps-clusterset`) is introduced, letting users declare cross-cluster replication between multiple GR-based MySQL clusters, as a MySQL InnoDB ClusterSet. A dedicated controller drives the ClusterSet orchestration using mysql-shell: bootstrap, switchover, rejoin, etc.

### 1.1 Goals

- Provide an API for managing MySQL InnoDB ClusterSets across sites.
- Support clusters anywhere reachable on the network: same K8s cluster, different K8s cluster, on-prem, managed service.
- Support planned switchover and explicit emergency (force) failover.
- Keep the per-site `PerconaServerMySQL` controller unchanged except for the minimum needed to let a new replica start as standalone. One of the requirements explicitly laid out by the MySQL docs is that replica clusters need to come up as 'standalone' clusters (no GR initialized). The ClusterSet takes care of initializing GR. Read about the requirements [here](https://dev.mysql.com/doc/mysql-shell/9.1/en/innodb-clusterset-requirements.html#innodb-clusterset-requirements-mysql-instances).

### 1.2 Non-Goals (Out of Scope)

- **Automatic switchover**: all switchover is declarative and user-initiated. Revisit once the manual flow is operationally proven and we trust the detection signal.
- **Automatic rejoin of INVALIDATED clusters**: Revisit once rejoin success/failure patterns are well understood from real operations.
- **Async-mode (Orchestrator) per-site topologies**: InnoDB ClusterSet supports GR only, mixing async and GR is not supported. Replication between async clusters requires a different design, possibly using native replication channels.
- **Adopting an existing ClusterSet** created out-of-band via mysqlsh: v1 either bootstraps a fresh ClusterSet or refuses if metadata is inconsistent.
- **MySQL Router integration**: while possible to set up MySQL Router to split read/write traffic between primary and DR, we will leave it out of scope for v1.

---

## 2. Background

### 2.1 Core Concepts

- **MySQL Group Replication (GR)**: MySQL's intra-cluster primary/secondary replication protocol. A GR group has one PRIMARY and N-1 SECONDARY members with synchronous-style consensus on transaction order. The existing operator already manages GR clusters when `spec.mysql.clusterType: group-replication`.
- **InnoDB Cluster**: A MySQL Shell abstraction over a GR group plus metadata stored in `mysql_innodb_cluster_metadata.*`. Created and managed via mysqlsh's `dba.createCluster()`, `cluster.addInstance()`, etc.
- **InnoDB ClusterSet**: A higher-level abstraction (introduced in MySQL 8.0.27) consisting of one PRIMARY InnoDB Cluster and N REPLICA InnoDB Clusters connected by async replication. Each REPLICA Cluster is itself a fully-functional GR group; only the PRIMARY Cluster accepts writes. Managed via mysqlsh's `dba.createClusterSet()`, `<cs>.createReplicaCluster()`, `<cs>.setPrimaryCluster()`, `<cs>.forcePrimaryCluster()`, `<cs>.rejoinCluster()`, `<cs>.removeCluster()`, `<cs>.dissolve()`, `<cs>.status()`.
- **CLONE plugin**: MySQL's built-in mechanism for taking a physical snapshot of one MySQL instance and transferring it to another. `createReplicaCluster()` uses CLONE to seed the replica from the primary. For TB-scale data over WAN, CLONE can take hours.
- **INVALIDATED state**: When mysqlsh's `forcePrimaryCluster()` runs while the old primary is unreachable, the old primary is marked INVALIDATED in metadata. It stays in the ClusterSet but is fenced off, because it may have accepted writes the new primary doesn't have. Recovery requires explicit `rejoinCluster()` (cheap, if GTIDs are compatible) or remove-and-recreate (slow, full re-CLONE).
- **mysqlsh**: The MySQL command-line tool that hosts the AdminAPI. Today the operator invokes mysqlsh via `kubectl exec` into a MySQL pod. For this feature, the controller invokes mysqlsh via `kubectl exec` into a dedicated `mysqlshell-runner` Pod whose image is configured per-ClusterSet (see §5.6).

### 2.2 Key Constraints

1. **InnoDB ClusterSet requires MySQL 8.0.27+** on every participating cluster, with a matching mysqlsh version. Versions below this are unsupported.
2. **The mysqlsh AdminAPI requires GR-compatible MySQL configuration** on every cluster and the CLONE plugin loaded. The per-site operator's existing bootstrap already produces this when `clusterType: group-replication`.
3. `cs.createReplicaCluster()` requires the target instance to be a clean standalone MySQL, not in any GR group, not in any ClusterSet. An existing operator-managed cluster bootstraps GR automatically on Pod-0, so we need a new bootstrap mode that skips this step for clusters intended to become ClusterSet replicas.
4. **mysqlsh's user that runs ClusterSet operations must exist on the target with the SAME credentials as on the primary**, because `createReplicaCluster` CLONEs the primary's `mysql.user` table over the replica's.
5. **StatefulSets generated by the operator do not set `PodManagementPolicy`**, so the Kubernetes default `OrderedReady` applies. This is needed for the standalone bootstrap path: Pod-1 and above must not start until Pod-0 is Ready, which only happens after a new cluster is formed from Pod-0.
6. **Backward compatibility:** New fields are additive and optional with defaults that preserve current behavior.
7. **The K8s cluster hosting the ClusterSet CR is not a SPOF for the underlying MySQL data**, because the controller is a pure orchestrator over MySQL primitives. Loss of the CR's K8s cluster does not lose MySQL data; redeploy and re-apply the CR to resume.

---

## 3. Architecture

### 3.1 Architecture Before This Change

The operator manages one MySQL cluster per `PerconaServerMySQL` CR. GR-mode clusters bootstrap themselves via the existing init-container flow:

```
Pod-0 boots
  → /opt/percona/bootstrap (cmd/bootstrap → cmd/bootstrap/gr)
    → configureInstance()                                  (mysqlsh, local exec)
    → connectToCluster() [fails: no peers]
      → peers.Len() == 1 → dba.createCluster()             (mysqlsh, local exec)
    → cluster.status() / addInstance() / rescan()
    → exits 0
  → Readiness/Liveness probes run
Pod-1, Pod-2... boot in OrderedReady sequence
  → connectToCluster() succeeds via Pod-0
  → addInstance() joins them to the existing GR group
```

There is no concept of:

- a CR spanning multiple clusters;
- a MySQL endpoint outside Kubernetes;
- the operator pod itself running mysqlsh against remote endpoints (today's `pkg/mysqlsh` always exec's into a MySQL pod);
- a MySQL pod coming up healthy without forming or joining a GR group.

### 3.2 Architecture After This Change

Two changes layered on top of the existing architecture:

**New CRD + controller (the bulk of the feature):**

```
User applies PerconaServerMySQLClusterSet CR
  → New controller in operator pod reconciles
    → Probe each clusters[].endpoints
    → Decide action: bootstrap | switchover | force-failover |
                     add-replica | remove | rejoin | refresh-status | dissolve
    → Execute:
       - Fast verbs via kubectl exec into the mysqlshell-runner Pod
       - createReplicaCluster as a Kubernetes Job using the runner image (initial cloning may take hours)
    → Update CR status from observed topology
```

The controller never speaks Kubernetes API to other clusters and does not know whether an endpoint is operator-managed.

**Per-site bootstrap mode (small change):**

```
spec.mysql.bootstrap.mode = manual (new field; default auto)
  Pod-0 bootstrap
    → configureInstance()
    → connectToCluster() [fails: no peers]
      → peers.Len() == 1 + manual mode → short-circuit, return 0
    → exits 0; pod stays NotReady (no GR membership yet)
  Pod-1, Pod-2 do not start (OrderedReady blocks on Pod-0 Readiness)
  --- external actor runs dba.createClusterSet().createReplicaCluster() ---
  Pod-0 is now a single-member GR group → ReadinessProbe passes
  Pod-1 boots → connectToCluster succeeds via Pod-0 → addInstance()
  Pod-2 boots → likewise
```

### 3.3 Key Observations

1. **InnoDB ClusterSet is a MySQL-side concept.** The natural CRD shape is a list of network endpoints with credentials, not a list of references to other CRs. This allows managing replicating from across K8S clusters or even on non-Kubernetes environments.
2. Long running operations such as `createReplicaCluster`, `setPrimaryCluster` and `removeCluster` shall be executed asynchronously via a Kubernetes Job. This ensures that the control loop is not blocked on long running operations.
3. **The `mysqlshell-runner` Pod is a new component.** Today the operator exec's into MySQL pods to run mysqlsh. For ClusterSet, the operator manages a separate long-running runner Pod (one per ClusterSet CR) and `kubectl exec`s mysqlsh commands into it. This keeps the operator image free of mysqlsh and lets each ClusterSet pin a mysqlsh version that matches its MySQL endpoints.

---

## 4. CRD and Interface Changes

### 4.1 CRD Spec Changes

**New CRD: `PerconaServerMySQLClusterSet`** (group `mysql.percona.com/v1`, scope `Namespaced`, short name `psclusterset`).

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLClusterSet
metadata:
  name: my-cluster-set
  namespace: default
spec:
  allowForceFailover: true
  primaryCluster: cluster1
  credentialsSecret:
    name: cluster1-credentials
    key: clusterset
  sslMode: AUTO
  mysqlshellRunner:
    image: percona/percona-server:8.0.36
  clusters:
  - name: cluster1
    endpoints:
    - host: 10.11.0.13
      port: 3306
  - name: cluster2
    endpoints:
    - host: 10.11.0.15
      port: 3306

```

- `spec.primaryCluster` *(required, string)*: Logical name of the desired primary cluster. Must equal exactly one `clusters[].name`. Editing this triggers a planned switchover, or a force failover if `allowForceFailover` is true and the current primary is unreachable.
- `spec.allowForceFailover` *(optional, default: `false`)*: When true, the controller is permitted to use `forcePrimaryCluster()` if a `primaryCluster` change is requested while the current primary is unreachable. When false, the controller blocks and surfaces a condition. Default `false` because force failover is destructive (data correctness can suffer).
- `spec.sslMode` *(optional, string, default: `AUTO`)*: SSL Mode for the ClusterSet replication channels. Available options are:
  - `DISABLED`: TLS encryption is disabled for the ClusterSet replication channels.
  - `REQUIRED`: TLS encryption is enabled for the ClusterSet replication channels.
  - `VERIFY_CA`:  like REQUIRED, but additionally verify the peer server TLS certificate against the configured Certificate Authority (CA) certificates.
  - `VERIFY_IDENTITY`: like VERIFY_CA, but additionally verify that the peer server certificate matches the host to which the connection is attempted.
  - `AUTO`: TLS encryption will be enabled if supported by the instance, otherwise disabled.
- `spec.mysqlshellRunner.image` *(required, string)*: Container image used for the `mysqlshell-runner` Pod and the `createReplicaCluster` Job. Must contain a `mysqlsh` binary on `PATH`. The mysqlsh version should match the major version of the MySQL endpoints participating in this ClusterSet (e.g. 8.0 endpoints → 8.0 mysqlsh). See §5.6 for the rationale.
- `spec.clusters[]` *(required, length >= 2)*: List of clusters participating in the ClusterSet. Each entry:
  - `name` *(required, immutable per entry)*: Logical handle used in `primaryCluster`, status, annotations. Must match `[a-z0-9-]{1,63}`. Immutable once observed in status; renames go through remove + re-add.
  - `endpoints[]` *(required, length >= 1)*: List of host:port pairs the controller can use to reach the cluster's MySQL members. Controller picks the first reachable. Multiple entries allow the controller's own connection to survive single-member failures.
  - `credentialsSecret` *(required, string)*: Name of a Secret in the same namespace, containing the `clusterset` user.

*Existing CRD* `PerconaServerMySQL` gains one new optional field:

- `spec.mysql.bootstrap.mode` *(optional, default: `auto`, enum: `auto` | `manual`)*: When set to `manual`, the cluster will not form a GR cluster on start. Pod-0 comes up and waits for the cluster to be created, in this case, done by the clusterset controller upon issuing `cs.createReplicaCluster(..)`

### 4.2 CRD Status Changes

`PerconaServerMySQLClusterSet.status`:

- `primaryCluster` (string): The cluster currently primary, as observed via `<cs>.status()`. May lag spec briefly during switchover.
- `lastObservedAt` (RFC3339 timestamp): Time of the last successful status refresh.
- `lastObservedGeneration`: The last observed generation of the ps-clusterset being reconciled
- `conditions[]` (Kubernetes-standard conditions):
  - `Ready` — ClusterSet is formed, replicas are replicating from specified primary.
  - `ClusterSetBootstrapped` - Primary is configured for forming a ClusterSet
  - `MysqlShellRunnerReady` — The mysqlshell runner Deployment is ready
  - `SwitchoverInProgress` — `spec.primaryCluster` differs from `status.currentPrimary` and reconcile is executing the change.
  - `PrimaryClusterUnreachable` - The primary cluster of this clusterset is not reachable by the operator
  - `ClusterSetDissolving` - The ClusterSet is deleting and dissolving all replica clusters
- `clusters[]` mirrors `<cs>.status()`'s output one-to-one so users can correlate `kubectl get psclusterset -o yaml` with mysqlsh output during incidents. Each entry:
  - `name`, `role` (`PRIMARY`/`REPLICA` from mysqlsh, not spec), `globalStatus` (`OK`/`OK_NOT_REPLICATING`/`NOT_OK`/`INVALIDATED`/`UNKNOWN`)

`PerconaServerMySQL`:

The following status conditions are added:

- `ClusterSetReplicationRunning` - when present, indicates that the cluster is a REPLICA member of a ClusterSet.
- `AwaitingExternalBootstrap` - when present and true, indicates that a cluster created using `.spec.mysql.bootstrap.mode=manual`, is awaiting bootstrap of GR, in this case, by a ClusterSet.

### 4.3 Internal Contracts

**clusterset user**: A new `clusterset` user is added into the existing set of users which shall be used for managing ClusterSet operations. Each replica cluster MUST contain the same set of passwords as in the primary before adding to the ClusterSet. Since the users will be replicated from the source cluster, it is also not possible to independently rotate credentials on replica clusters.

**Finalizer**: `mysql.percona.com/clusterset-dissolve` is added to `psclusterset` object, which ensures that the ClusterSet dissolves when the object is deleted from K8S. This only stops replication and deletes metadata, the actual clusters continue to run.

**Env plumbing for the per-site bootstrap mode** (added by the per-site operator to the mysqld container):

- `BOOTSTRAP_MODE` *(values: `auto` | `manual`)* — always set; read by both `cmd/bootstrap` and `cmd/healthcheck`.

**mysqlshell-runner Deployment contract**:

- Name pattern: `<cr-name>-mysqlshell-runner`.
- One Pod per ClusterSet CR, in the CR's namespace, managed by the controller (created on first reconcile, automatically cleaned up using ControllerReferences).
- Image: `spec.mysqlshellRunner.image`. Must provide `mysqlsh` on `PATH`.
- Long-lived and stateless: no PVCs, no Secrets baked in. The controller passes credentials per invocation via stdin/env when it `kubectl exec`s into the Pod.
- Used for every mysqlsh AdminAPI call the controller makes.

**Job-based contract for long running operations:**

- Job name pattern: `<cr-name>-<cluster-name>-<action>`. This naming ensures no 2 jobs can run the same action for the same cluster.
- Uses the same image as the operator
- Execs into the `mysqlshell-runner` for performing long-running ClusterSet operations.

### 4.4 User-Facing Behavior Changes

- New resource visible via `kubectl get psclusterset`, with printed columns `Primary`, `Endpoint` and `Ready`.
- New events for `PerconaServerMySQLClusterSet`:
  - `ClusterSetPrimarySwitched`
  - `ClusterSetPrimaryForceSwitched`
  - `ClusterSetBootstrapped`
  - `ClusterSetHealthDegraded`
  - `ClusterSetMemberAdded`
  - `ClusterSetMemberRemoved`
- For `PerconaServerMySQL` CRs with `spec.mysql.bootstrap.mode: manual`, Pod-0 will stay `NotReady` indefinitely until bootstrapped by the `psclusterset` controller . This is intentional and surfaced via the Readiness Probe failing with "Member state: OFFLINE" until the ClusterSet controller (or some other actor) attaches it. A new status condition surfaces this scenario - `AwaitingExternalBootstrap`

---

## 5. Design Decisions and Alternatives

### 5.1 ClusterSet and not native replication channels

**Chosen:** Use MySQL InnoDB ClusterSet instead of native replication channels, as used by the PXC Operator.

**Why:** Native replication channels do not align well with Group Replication because they operate outside the Group Replication metadata model. Supporting them would require the operator to work around MySQL's topology metadata, reconcile state manually, and accept functional limitations, including limited replication of MySQL users and grants.

### 5.2 Dedicated CRD for cross-cluster replication

**Chosen:** A dedicated CRD `PerconaServerMySQLClusterSet` for setting up cross-cluster-replication.

**Why:** The thing users operate is not an individual MySQL cluster; it is the replication topology across clusters. A ClusterSet has topology-wide invariants: exactly one writable primary Cluster, zero or more replica Clusters, a single source of truth for the current primary, and mutating verbs such as `createReplicaCluster`, `setPrimaryCluster`, `forcePrimaryCluster`, `rejoinCluster`, and `dissolve`. Those operations are not owned by any one `PerconaServerMySQL` CR.

Embedding ClusterSet configuration into every participating `PerconaServerMySQL` CR would split one MySQL-side object across multiple Kubernetes objects. For example:

```yaml
# Primary cluster
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: primary-cluster
spec:
  # ...
  clusterSet:
    role: primary
---
# Replica cluster
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: replica-cluster
spec:
  # ...
  clusterSet:
    role: replica
    primaryEndpoints:
    - host: 10.0.1.13
      port: 3306
```

To perform a planned switchover with this shape, the desired primary role would need to move from one CR to another. Kubernetes does not give us an atomic transaction across two independent CR updates, and in the cross-cluster case those objects may not even live in the same Kubernetes cluster. Any partially-applied change creates an ambiguous desired state: two clusters may claim to be primary, no cluster may claim to be primary, or the MySQL ClusterSet may have switched successfully while the Kubernetes objects still describe the old topology.

A dedicated `PerconaServerMySQLClusterSet` CR keeps the topology in one object. The spec declares the participating endpoints and the requested topology-level operation; the status records the observed ClusterSet state, including the current primary and invalidated replicas. This gives the controller one reconciliation boundary for actions that must be serialized against the MySQL AdminAPI.

**Alternatives considered:**


| Alternative                                                | Why Rejected                                                                                                                                                                                                 |
| ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Use `.spec.clusterSet` on existing `PerconaServerMySQL` CR | Splits one ClusterSet across N independently reconciled CRs, requires non-atomic role changes for switchover/failover, and forces per-site cluster CRs to own topology-level operations they do not control. |


### 5.3 Network-only coupling (no binding to `PerconaServerMySQL` CRs)

**Chosen:** The ClusterSet CR references MySQL endpoints + credentials. It does **not** reference `PerconaServerMySQL` CRs by name and does not use ownerReferences.

**Why:** MySQL InnoDB ClusterSet is fundamentally a MySQL-side construct. Coupling to a Kubernetes-native parent introduces an artificial boundary that excludes legitimate use cases (on-prem MySQL, managed services, mixed environments). All cross-site work happens over the MySQL protocol via mysqlsh; the K8s API is not involved in cross-cluster operations.

**Alternatives considered:**


| Alternative                                                              | Why Rejected                                                                                                                                                                    |
| ------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `clusters[].clusterRef.name` pointing at a local `PerconaServerMySQL` CR | Excludes non-K8s endpoints; introduces a coupling that has no operational meaning (the per-site operator and the ClusterSet operator never need to coordinate via K8s objects). |
| Owner-references creating per-site CRs from a ClusterSet template        | Even tighter coupling; impossible to use with pre-existing clusters; impossible to use with non-K8s endpoints.                                                                  |


### 5.4 Single-instance, namespaced controller (no replication, no leader election)

**Chosen:** The CR is namespaced. The operator in that namespace (or a cluster-wide operator) reconciles it as a normal Kubernetes resource. Only one CR per logical ClusterSet, in one K8s cluster.

**Why:** With network-only coupling, the controller does not need co-location with any specific cluster.

### 5.5 Jobs for long running operations

**Chosen:** All long running ClusterSet operations are run asynchronously via Kubernetes Jobs, and the controller tracks their completions.

**Why:**: Avoids blocking the control loop on long running operations.

**Alternatives considered:**


| Alternative                            | Why Rejected                                                              |
| -------------------------------------- | ------------------------------------------------------------------------- |
| Run in a goroutine, in-memory tracking | Pod restart loses progress; in-memory state is fragile across reconciles. |


### 5.6 Bootstrap "manual" mode via a single-branch short-circuit

**Chosen:** Add `spec.mysql.bootstrap.mode: auto | manual`. In `manual` mode, the existing GR bootstrap short-circuits at the `peers.Len() == 1 && connectToCluster failed` branch, returning 0 before calling `dba.createCluster()`. Other probes (readiness, liveness) handle the resulting unadopted state.

**Why:** Minimum invasive change to the existing bootstrap. Pod-1+ already work correctly because their `connectToCluster` succeeds via Pod-0 after adoption; the existing `addInstance` flow handles the rest. ReadinessProbe is naturally accurate (empty `replication_group_members` → fails → pod NotReady). Only the LivenessProbe needs modification to tolerate the unadopted state indefinitely (instead of triggering container restart). Full design in `auto-bootstrap.md`.

**Alternatives considered:**


| Alternative                                                             | Why Rejected                                                                                                                                                                                 |
| ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| New `clusterType: clusterset-replica` distinct from `group-replication` | Bloats the cluster-type enum with what is really a one-line bootstrap variation. Would also break the symmetry needed when a replica eventually becomes primary.                             |
| StartupProbe loops forever pre-adoption                                 | Probes have a `failureThreshold × periodSeconds` budget; after that the container restarts. Adoption has no clock.                                                                           |
| Clamp StatefulSet replicas to 1 while standalone, scale up on adoption  | Adds reconcile complexity for no real benefit; OrderedReady already gives the same effective sequencing for free, and Pod-1+ wait costs zero compute (they don't exist as running pods yet). |


### 5.7 mysqlsh runtime: dedicated `mysqlshell-runner` Pod, invoked via `kubectl exec`

**Chosen:** The controller manages a long-lived `mysqlshell-runner` Pod per ClusterSet CR. Its image is configured via `spec.mysqlshellRunner.image` and must contain the `mysqlsh` binary. The controller `kubectl exec`s mysqlsh AdminAPI calls into this Pod against the remote MySQL endpoints declared in `spec.clusters[]`. The operator image itself does not contain mysqlsh.

**Why:**

- **Decouples mysqlsh version from operator releases.** mysqlsh needs to match the major version of the MySQL endpoints it talks to (8.0 vs 8.4 and onward). Bundling mysqlsh in the operator image would tie every operator release to one mysqlsh version and prevent users from pinning a different one per ClusterSet. With a separate runner image, each ClusterSet pins its own version via `spec.mysqlshellRunner.image`.
- **Keeps the operator image slim.** Bundling mysqlsh adds ~80-150 MB to the operator image, paid by every operator deployment regardless of whether ClusterSet is in use.
- **Acceptable cost.** The runner Pod is a single stateless container per ClusterSet CR — no PVCs, no Secrets baked in, nothing to lose on restart. `kubectl exec` latency is fine for the dozens of probe calls per minute under steady-state reconcile.

**Alternatives considered:**


| Alternative                                                | Why Rejected                                                                                                                                                                                          |
| ---------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Bundle mysqlsh in the operator image, invoke via `os/exec` | Couples operator releases to mysqlsh release cadence; image grows ~80-150 MB for every user; no way to pin a different mysqlsh version per ClusterSet, which matters when CRs span 8.0 and 8.4 MySQL. |
| Short-lived Job per mysqlsh invocation                     | Adds pod-start latency to every operation; for the dozens of probe calls per minute under steady-state reconcile load, this is unacceptable.                                                          |
| Exec into a local MySQL pod for mysqlsh                    | Doesn't work when the primary cluster is outside K8s or outside the managing controller's cluster.                                                                                                    |


### 5.8 Dedicated `clusterset` user

**Chosen:** A new MySQL user `clusterset` with the required grants set is created on every operator-managed cluster, alongside the existing system users. Password sourced from a new key in the per-site Secret. The ClusterSet CR's `credentialsSecret` references a Secret whose password matches.

**Why:** The existing `operator` user has `GRANT ALL ON *.* WITH GRANT OPTION` and would work, but reusing it for cross-site administration conflates privileges and grant scopes. The existing `replication` user lacks DML grants on schemas outside `mysql.`*, so it cannot manage the `mysql_innodb_cluster_metadata` schema that mysqlsh ClusterSet operations write to. A dedicated user with exactly the needed grants follows least-privilege and gives users a clean audit/rotation surface for cross-site credentials.

Provisioning during initial bootstrap keeps the ClusterSet controller free of any privileged user-management work, consistent with the "controller doesn't touch users on endpoints" framing.

**Alternatives considered:**


| Alternative                                               | Why Rejected                                                                                                                             |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Reuse existing `operator` user                            | Conflates privilege scopes; rotation/audit becomes coarse-grained.                                                                       |
| Reuse existing `replication` user                         | Missing DML grants on `mysql_innodb_cluster_metadata.`*; would fail on the very first mysqlsh ClusterSet call.                           |
| ClusterSet controller provisions the user just-in-time    | Adds privileged user-management to the controller                                                                                        |
| User provisions the account manually outside the operator | High friction; users may get the grant set wrong. The operator already provisions five other system users; one more follows the pattern. |


### 5.9 Manual-only switchover with explicit `allowForceFailover` opt-in

**Chosen:** No auto-failover. Switchover requires the user to edit `spec.primaryCluster`. Force failover additionally requires `spec.allowForceFailover: true`.

**Why:** Force failover destroys correctness guarantees if performed when the old primary is alive but unreachable from the controller only. The cost of staying down a few extra minutes during an incident is much smaller than the cost of an incorrect force-failover. Two separate signals: the spec edit and the permission flag are required to make sure both are deliberate.

**Alternatives considered:**


| Alternative                                                                         | Why Rejected                                                                                                                                                                |
| ----------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Auto-failover with `spec.failover.automatic: true` and tunable guardrails           | Detection signal across sites is difficult, we don't yet have operational evidence to trust an automated detector. Revisit in a future release.                             |
| Single `allowUnsafeFailover` boolean covering both lag tolerance and force-failover | Conflates independent decisions; users would surprise themselves in incidents. The chosen design keeps the two knobs explicit; lag is left to mysqlsh to enforce or refuse. |


### 5.10 Finalizer dissolves the ClusterSet on CR deletion

**Chosen:** A `mysql.percona.com/clusterset-dissolve` finalizer runs `<cs>.dissolve()` on CR deletion (after waiting for any in-flight `createReplicaCluster` Job). All underlying clusters revert to standalone InnoDB Clusters; data is preserved; per-site operators continue to manage them.

**Why:** Matches Kubernetes-idiomatic cascade-on-delete semantics (`kubectl delete` actually deletes the modeled thing). The underlying MySQL clusters are not destroyed — only their ClusterSet association is. Per-site operators continue to manage them. An annotation-based bypass exists for the case where the primary cluster is permanently unreachable and the dissolve cannot run.

**Alternatives considered:**


| Alternative                                         | Why Rejected                                                                                                                          |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------- |
| No finalizer; delete leaves MySQL ClusterSet intact | Surprising to users who expect `kubectl delete` to delete the modeled thing; orphaned cross-site replication keeps running invisibly. |


---

## 6. Replication Model Impact

This feature is **GR-only**. It does not apply to clusters with `spec.mysql.clusterType: async`.

### 6.1 Group Replication Behavior

A GR cluster participates in a ClusterSet by:

- Being the **primary** cluster: accepts writes, replicates async to replica clusters. Its own GR membership is unchanged.
- Being a **replica** cluster: serves reads only (`SUPER_READ_ONLY=ON` via mysqlsh), receives async replication from the primary cluster's PRIMARY member, maintains its own internal GR for HA within the replica site.

A new GR cluster intended to become a replica cluster sets `spec.mysql.bootstrap.mode: manual` so its Pod-0 does not auto-form a GR group; `createReplicaCluster()` does that as part of its work.

### 6.2 Async Replication Behavior

Not supported. A `PerconaServerMySQL` CR with `clusterType: async` cannot participate in a ClusterSet. Validation: the controller refuses to operate on an endpoint that does not respond as an InnoDB Cluster or as a clean standalone GR-capable instance.

Cross-cluster replication support may be added in the future using native replication channels.

---

## 7. User Experience

### 7.1 Existing CR (Unchanged)

A `PerconaServerMySQL` CR without `spec.mysql.bootstrap` is identical to today. Defaulting treats it as `mode: auto`:

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: cluster1
spec:
  mysql:
    clusterType: group-replication
    size: 3
  # ... unchanged ...
```

### 7.2 Standalone replica cluster (precursor to ClusterSet membership)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: cluster2
spec:
  mysql:
    clusterType: group-replication
    size: 3
    bootstrap:
      mode: manual                  # NEW: Pod-0 boots standalone, no dba.createCluster()
  secretsName: cluster2-creds
  # ... rest unchanged ...
```

Pod-0 starts, configures MySQL for GR, creates `clusterset` and the other system users, then exits its bootstrap successfully without forming GR. ReadinessProbe fails (no GR membership), pod stays NotReady. Pod-1 and Pod-2 are blocked because of `OrderedReady`.

### 7.3 Creating the ClusterSet

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLClusterSet
metadata:
  name: my-clusterset
spec:
  primaryCluster: cluster1
  allowForceFailover: false
  sslMode: AUTO
  credentialsSecret:
    name: cluster1-secret
  mysqlshellRunner:
    image: percona/percona-server:8.0.36
  clusters:
    - name: cluster1
      endpoints:
        - host: cluster1-mysql.example.com
          port: 3306
    - name: cluster2
      endpoints:
        - host: cluster2-mysql.example.com
          port: 3306
```

- Controller probes cluster1 (already an InnoDB Cluster), runs `dba.createClusterSet('my-clusterset')` inline. 
- Probes cluster2 (clean standalone), creates a Job that runs `createReplicaCluster`.
- When the Job completes, cluster2 is a single-member GR replica cluster within the ClusterSet; Pod-0's ReadinessProbe now passes; OrderedReady lets Pod-1 and Pod-2 boot and join the local GR via the existing `addInstance` path.
- ClusterSet is ready

#### 7.3.1 Seeding using an existing backup

By default, `createReplicaCluster()` seeds the first member of a replica cluster with the MySQL `clone` recovery method. That is convenient for small datasets and low-latency networks, but it is a poor fit for WAN links and multi-TB datasets where a full online clone can take a long time and is expensive to retry.

For large datasets, seed the replica cluster from a backup taken from the current primary cluster before adding it to the ClusterSet:

1. Create the replica `PerconaServerMySQL` with `spec.mysql.bootstrap.mode: manual`.
2. Restore the source backup into the replica cluster.
3. Add the replica cluster to `PerconaServerMySQLClusterSet`.

The restore targets the replica cluster, not the primary:

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLRestore
metadata:
  name: clusterset-seed
spec:
  clusterName: cluster2
  backupSource:
    destination: s3://my-bucket/backups/cluster1-full
    storage:
      type: s3
      s3:
        bucket: my-bucket
        credentialsSecret: s3-credentials
        region: us-east-1
```

After the restore succeeds, add `cluster2` to `PerconaServerMySQLClusterSet`. Because the target already contains data and GTID history from the primary cluster, we can use the `incremental` recovery instead of transferring the full dataset with `clone`.

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQLClusterSet
metadata:
  name: my-clusterset
spec:
  # ...
  createReplicaClusterOptions:
    recoveryMethod: incremental
```

### 7.4 Planned switchover

User edits `spec.primaryCluster: cluster2`. Controller checks status (both clusters reachable), runs `setPrimaryCluster('cluster2')` . `status.currentPrimary` updates to `cluster2`.

### 7.5 Force failover

cluster1 has become unreachable. User edits:

```yaml
spec:
  primaryCluster: cluster2
  allowForceFailover: true        # explicit opt-in
```

Controller runs `forcePrimaryCluster('cluster2')` inline. cluster1 becomes INVALIDATED; status surfaces `ClusterInvalidated` condition with a hint about the rejoin path.

### 7.6 Removal of a replica cluster

A replica cluster can be removed from a ClusterSet by deleting its entry from `.spec.clusters[]`. During reconciliation, the ClusterSet controller removes the cluster from the ClusterSet, dissolves Group Replication on the removed cluster, and lets the per-site operator bootstrap it again as a standalone InnoDB Cluster.

Removal is one-way: after a cluster has been removed from a ClusterSet, it cannot be added back to the same ClusterSet.

### 7.7 Deletion

`kubectl delete psclusterset my-clusterset`. Finalizer waits for any in-flight `createReplicaCluster` Job to terminate, then runs `<cs>.dissolve()` inline. CR is deleted. Both underlying clusters revert to standalone InnoDB Clusters and remain managed by their per-site operators.

---

## 8. Error Handling and Edge Cases

### 8.1 Primary endpoint unreachable at bootstrap

**Scenario:** ClusterSet CR is applied but the cluster named in `spec.primaryCluster` is unreachable from the controller.

**Expected behavior:** Set `ClusterSetBootstrapped` condition to `False` with reason and message from the error. Requeue with backoff; surface the condition until the user fixes connectivity.

### 8.2 Replica endpoint is not a clean standalone

**Scenario:** A cluster listed in `clusters[]` (non-primary) probe result shows it is already part of some other InnoDB Cluster or ClusterSet.

**Expected behavior:** Set `Ready` condition to `False` with reason `ReplicaNotStandalone`. Do not queue a `createReplicaCluster` Job as mysqlsh would refuse anyway, and a silent force-remove would risk data loss.

### 8.3 `createReplicaCluster` Job fails

**Scenario:** The Job exits non-zero (CLONE failed, network drop mid-operation, mysqlsh rejected for any reason).

**Expected behavior:** Surface `ReplicaManagementFailure` condition with with `True` and the Job's stderr captured. Do not retry automatically. User can re-trigger by `kubectl delete job`, controller recreates on next reconcile. If the failure is environmental (unreachable target), user fixes the environment first.

### 8.4 Primary unreachable during planned switchover

**Scenario:** User edits `spec.primaryCluster: cluster2`, `allowForceFailover: false`, and cluster1 (old primary) is unreachable.

**Expected behavior:** Set `SwitchoverFailed` and `PrimaryUnreachable` conditions. Do nothing. User either fixes cluster1 (reconcile naturally proceeds) or flips `allowForceFailover: true` (next reconcile uses `forcePrimaryCluster`).

### 8.5 Dissolve fails during finalizer

**Scenario:** User deletes the CR; finalizer's `<cs>.dissolve()` call fails because the primary is unreachable or mysqlsh errors.

**Expected behavior:** Log errors, retry on next reconcile. CR remains with `deletionTimestamp` set. User must fix the issue and let the finalizer succeed.

---

## 9. Migration and Backward Compatibility

### 9.1 Existing Clusters

- `PerconaServerMySQL` CRs without `spec.mysql.bootstrap` continue to work as usual.
- Existing system-user Secrets gain a new key `clusterset` on next reconcile after operator upgrade (gated by CRVersion check); the per-site operator generates the password if absent and creates the user via `ps-entrypoint.sh` on subsequent pod restarts. Already-running pods do not have `clusterset` until they restart; this is acceptable because the user is only needed for ClusterSet participation, which is an explicit opt-in.

### 9.2 CRD Compatibility

- The new `PerconaServerMySQLClusterSet` CRD is purely additive.
- The new `spec.mysql.bootstrap` field on `PerconaServerMySQL` is additive, optional, with a default that preserves current behavior.
- New status conditions are additive.

### 9.3 Operator Version Skew

If the operator pod is upgraded but a `PerconaServerMySQL` CR is not re-applied:

- Defaulting fills `bootstrap.mode: auto` on first reconcile.
- Running pods (already in a GR cluster) are unaffected — the new bootstrap branch is only taken on first start.
- A pod restart during the upgrade window does not change behavior; `auto` mode is byte-identical to today's path.

---

## 10. Testing Strategy

### 10.1 E2E Test Scenarios


| Scenario                                                | Cluster Type | What It Validates                                                                                                                                                                                                                                                               |
| ------------------------------------------------------- | ------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| ClusterSet bootstrap from two operator-managed clusters | GR           | Apply primary CR, then replica CR with `bootstrap.mode: manual`, then ClusterSet CR. Validate: `<cs>.status()` returns OK; replica's Pod-0 becomes Ready after `createReplicaCluster` Job succeeds; replica's pods 1,2 boot via OrderedReady; status mirrors observed topology. |
| Planned switchover                                      | GR           | Edit `spec.primaryCluster`. Validate: `SwitchoverInProgress` condition transitions through True → False; `status.currentPrimary` updates; data written to old primary pre-switchover is visible on new primary post-switchover.                                                 |
| Force failover with old primary down                    | GR           | Kill the primary cluster's pods; set `allowForceFailover: true`; edit `spec.primaryCluster`. Validate: `ClusterInvalidated` condition surfaces for old primary; new primary accepts writes; bringing old primary back does not auto-rejoin.                                     |
| Annotation-driven rejoin after force failover           | GR           | Apply rejoin annotation to a previously-INVALIDATED cluster. Validate: condition clears; data flows; annotation is removed by the controller.                                                                                                                                   |
| Deletion finalizer dissolves cleanly                    | GR           | Delete the CR. Validate: finalizer waits for any in-flight Job; runs `dissolve`; CR is removed; underlying clusters revert to standalone InnoDB Clusters and continue accepting traffic.                                                                                        |


---

## 11. Open Questions

*None at this time.*

### Resolved

- **mysqlsh runtime / version tracking.** Previously open: whether to bundle MySQL Shell in the operator image, given that the operator would otherwise need to track multiple mysqlsh versions (8.0, 8.4, …) and rebuild on every mysqlsh release. **Resolution:** the controller does not bundle mysqlsh. Instead, it manages a dedicated `mysqlshell-runner` Pod per ClusterSet CR, with the image configured via `spec.mysqlshellRunner.image`, and invokes mysqlsh via `kubectl exec` into that Pod. The `createReplicaCluster` Job uses the same image. See §5.6.

---

## Appendix

### A. Glossary


| Term              | Definition                                                                                                  |
| ----------------- | ----------------------------------------------------------------------------------------------------------- |
| GR                | Group Replication — MySQL's intra-cluster replication protocol                                              |
| InnoDB Cluster    | mysqlsh's abstraction over a GR group + metadata schema                                                     |
| InnoDB ClusterSet | mysqlsh's higher-level abstraction: one primary InnoDB Cluster + N async-replicated replica InnoDB Clusters |
| CLONE             | MySQL's physical-snapshot data transfer mechanism, used by `createReplicaCluster`                           |
| INVALIDATED       | A ClusterSet member state indicating possible GTID divergence; fenced off until manual rejoin               |
| CS-ctlr           | The new ClusterSet controller introduced by this design                                                     |
| PS-ctlr           | The existing `PerconaServerMySQL` controller                                                                |
| OrderedReady      | Kubernetes StatefulSet policy that starts Pod-N+1 only after Pod-N becomes Ready (the default)              |


### B. References

- [InnoDB ClusterSet docs](https://dev.mysql.com/doc/mysql-shell/9.1/en/innodb-clusterset.html)

