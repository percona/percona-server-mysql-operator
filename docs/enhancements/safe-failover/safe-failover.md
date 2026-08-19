# K8SPS-842: Safe failover with no data loss for async clusters

| Field        | Value      |
|--------------|------------|
| Author       | Ege Güneş  |
| Status       | Draft      |
| Created      | 2026-08-19 |
| Last Updated | 2026-08-19 |
| Reviewers    |            |

---

## 1. Overview

When the primary of an async cluster dies, Orchestrator promotes the most advanced
replica. Any transaction that reached the primary's binary log but not that replica is
lost, silently. This feature makes the failover lossless: the failed primary's binary
logs are salvaged, the missing transactions are replayed into the successor over a
normal replication channel, and the successor is not allowed to accept a single write
until it holds every transaction the old primary acknowledged.

The guarantee is conditional: it holds whenever the failed primary's binary logs are
readable. When its storage is unreachable, no mechanism can recover those transactions,
and the operator's job is to say so clearly and let the user choose between waiting and
accepting loss.

### 1.1 Goals

- Lose no acknowledged transaction when failing over an async cluster, whenever the
  failed primary's binary logs are readable.
- Complete recovery in under two minutes in the common case (primary's pod alive,
  `mysqld` dead).
- Keep the old primary fenced for the whole process: not writable, not Ready, not in
  any proxy backend, not re-attachable by Orchestrator.
- Keep the successor at `super_read_only=1` until it holds the complete delta.
- Give the user an explicit, auditable way to bypass the wait and accept data loss.
- Extend the same guarantee to planned switchover, which today trusts Orchestrator's
  word rather than verifying it.

### 1.2 Non-Goals (Out of Scope)

- **Group Replication.** GR has its own consistency model and a different promotion
  path. This design is entirely within the Orchestrator-driven async path.
- **Zero loss when the failed primary's storage is permanently destroyed.** Physically
  impossible with async replication. Handled as an explicit user decision.
- **Concurrent failovers.** One safe failover in flight at a time; a second detection
  blocks and requires human resolution. Revisit once single-incident behavior is
  proven in the field.
- **Force-deleting pods on unreachable nodes.** The operator cannot know a node is
  truly gone. Kubernetes already has an admin-driven mechanism for that assertion
  (`node.kubernetes.io/out-of-service`) and the operator surfaces it instead.

---

## 2. Background

### 2.1 Core Concepts

- **Async replication topology.** With `spec.mysql.clusterType: async`, one MySQL pod
  is the primary and the rest are replicas. Orchestrator owns topology detection and
  failover. HAProxy discovers the primary by probing every node with an external check
  and routing writes to whichever one reports `read_only=0`.

- **GTIDs and auto-position.** Every transaction carries a globally unique identifier.
  A replica configured with `SOURCE_AUTO_POSITION=1` tells the source its
  `gtid_executed` on connect, and the source sends everything the replica does not
  have. The delta computation this feature needs is therefore a native protocol
  feature, not something we have to implement.

- **The binary log is the record of durability.** MySQL commits through two-phase
  commit between InnoDB and the binary log. With `sync_binlog=1` a client's `COMMIT`
  is acknowledged only after the binary log is fsynced, and crash recovery rolls back
  anything prepared but not binlogged. So "the transactions in the binary log" is
  exactly "the transactions the application was told succeeded" — and exactly what a
  replica would have received. This equivalence is the correctness argument for the
  whole feature.

- **Orchestrator recovery hooks.** Orchestrator runs configured shell commands at
  fixed points in a recovery. The operator uses four of them today, all invoking
  `orc-handler` to move the primary pod label. Two more matter here:
  `OnFailureDetectionProcesses` (fires on detection, before the decision) and
  `PreFailoverProcesses` (fires after the decision, before promotion; a non-zero exit
  aborts the recovery).

  Three properties of the Percona fork matter and are not obvious from the
  documentation. Hooks have **no execution timeout** — `os.CommandRun` blocks on
  `cmd.CombinedOutput()` — so a hook may block a recovery indefinitely and must
  impose its own deadline. **Postponed functions run last**, after every `Post*` hook,
  which is when `MasterFailoverDetachReplicaMasterHost` takes effect. And a graceful
  takeover is implemented as a forced `DeadMaster` recovery, so
  **`PreFailoverProcesses` fires on planned switchovers too**; the `{command}`
  placeholder carries the `CommandHint` and is how a hook tells the two apart.

- **The sidecar.** When `spec.backup.enabled` is true — the default — every MySQL pod
  runs an `xtrabackup` container executing `cmd/sidecar`, which mounts
  `/var/lib/mysql` and the users secret and serves HTTP on port 6450. Crucially it is a separate container from `mysqld`, so it stays up when
  `mysqld` is dead or crash-looping. It is the only process that can reach a dead
  primary's data directory.

- **Fencing.** Making a node incapable of accepting writes and invisible to clients:
  `super_read_only=1`, `offline_mode=ON`, failing readiness so the pod leaves Service
  endpoints and HAProxy's peer list, and an Orchestrator downtime so it is not
  re-attached to the topology.

### 2.2 Key Constraints

1. **Only the old primary's binary logs prove completeness.** No other source can tell
   us what the primary committed. The PITR binlog server is itself an asynchronous
   consumer and cannot report what it missed. Therefore the zero-loss guarantee always
   requires access to the old primary's volume, and every "the node is gone" scenario
   reduces to a user decision.

2. **`PreFailoverProcesses` can abort but does not know the successor;
   `PostMasterFailoverProcesses` knows the successor but cannot abort.** Candidate
   selection happens between them. Any design must pick a side or work around it.

3. **Orchestrator is not multi-source aware.** It reads the first row of
   `SHOW SLAVE STATUS`. Adding a second replication channel to an instance under
   Orchestrator's supervision risks misparsed topology.

4. **The sidecar exists only when `spec.backup.enabled` is true**
   (`pkg/mysql/mysql.go:607`). Anything that lives in the sidecar inherits that
   condition.

5. **PVCs are ReadWriteOnce.** A volume attaches to one node at a time, though
   multiple pods on that node may mount it. After node loss the volume stays attached
   to the dead node until Kubernetes force-detaches it.

6. **Kubernetes never force-deletes pods on unreachable nodes.** This is deliberate:
   the StatefulSet controller preserves at-most-one semantics. The pod stays
   `Terminating` until the node object is deleted or an admin applies the
   `node.kubernetes.io/out-of-service` taint.

7. **Write availability must not depend on the operator being alive.** Today
   Orchestrator restores writes on its own. A design where the operator is a required
   participant in every promotion is an availability regression.

8. **CEL validation applies on update.** A new CEL rule that existing CRs violate
   hard-blocks every subsequent update to those CRs after an operator upgrade.

9. **PS 8.4 is the default image** (`deploy/cr.yaml:72`) and has removed
   `mysql_native_password`. Anything a replica authenticates against must support
   `caching_sha2_password`.

10. **HAProxy's external check runs at `inter 10000`.** Up to ten seconds elapse
    between a node becoming writable and HAProxy routing writes to it. This is inside
    the two-minute budget and outside our control without retuning the check.

---

## 3. Architecture

### 3.1 Architecture Before This Change

```
primary dies
  → Orchestrator detects DeadMaster (InstancePollSeconds: 5)
    → regroups replicas, picks the most advanced one
    → ApplyMySQLPromotionAfterMasterFailover: true
         RESET SLAVE ALL; read_only=0        ← writable immediately
    → PostMasterFailoverProcesses
         orc-handler -primary {successorHost}  ← moves the pod label
  → HAProxy's external check finds the new writable node
  → writes resume; anything the old primary had and the successor did not is gone
```

The old primary is not fenced. If it comes back it is discovered and re-attached, and
whatever it had that the successor lacks becomes an errant transaction.

### 3.2 Architecture After This Change

Two new pieces. A **failover agent** inside the existing sidecar container, which can
fence its pod and serve that pod's binary logs to a replica over the MySQL replication
protocol. And a shared **`pkg/failover`** package holding the sequence logic, driven
both by `orc-handler` (the fast path, invoked from hooks) and by the operator's
reconcile loop (the backstop, and the owner of timeout and bypass policy).

```
primary dies
  → Orchestrator detects DeadMaster
    → OnFailureDetectionProcesses: orc-handler on-failure-detection
         records the incident: status + event. No side effects.
    → PreFailoverProcesses: orc-handler pre-failover -failed {failedHost}
         a. Orchestrator BeginDowntime(failedHost)
         b. sidecar agent writes /var/lib/mysql/.failover-fence
         c. if mysqld reachable: super_read_only=1, offline_mode=ON, kill clients
         d. readiness now fails → pod leaves endpoints → leaves HAProxy
         e. sidecar agent starts the binlog source on :33065 and reports the old
            primary's complete binlog GTID set
         exit non-zero (aborting the failover) only if the pod is present and
         cannot be fenced. An absent pod is fenced by absence.

    → Orchestrator regroups, promotes the most advanced replica
         ApplyMySQLPromotionAfterMasterFailover: false
              → successor stays super_read_only=1        ← THE GATE
              → no RESET SLAVE ALL: its channel still names the dead master
         MasterFailoverDetachReplicaMasterHost: true
              → postponed; fires only after every Post* hook (see 8.10)
         most replicas are repointed to the successor during regroup

    → PostMasterFailoverProcesses: orc-handler post-master-failover
                                     -failed {failedHost} -successor {successorHost}
         RESET REPLICA ALL
         CHANGE REPLICATION SOURCE TO
             SOURCE_HOST=<failed-pod>.<cluster>-mysql-unready.<ns>,
             SOURCE_PORT=33065, SOURCE_AUTO_POSITION=1, SOURCE_SSL=1
         START REPLICA
         poll SELECT GTID_SUBSET(<target>, @@GLOBAL.gtid_executed)
         STOP REPLICA; RESET REPLICA ALL; stop the source
         super_read_only=0; read_only=0
         move the primary pod label
  → HAProxy's check finds a writable primary; writes resume with the full delta
```

During the catch-up window `haproxy_check_primary.sh` passes on no node — the
successor fails it on both `super_read_only` and on having replication running — so
the `mysql-primary` backend has no server UP and writes fail fast rather than reaching
a node that would silently reject or, worse, accept them.

### 3.3 Key Observations

1. **The gate is state, not a hook's exit code.** What prevents writes is the
   successor being `super_read_only=1`, a property of the database. If the hook
   process is killed, the node reboots, or the operator restarts, the cluster stays
   read-only and either driver resumes. There is no window in which a partially
   caught-up primary is writable.

2. **After promotion the successor's default replication channel is defunct but not
   yet detached.** With the promotion apply skipped, Orchestrator issues no
   `RESET SLAVE ALL`, and the detach is postponed until after all `Post*` hooks. The
   channel still names the dead master and will never connect, so we are free to
   `RESET REPLICA ALL` and reuse it. Constraint 2.2.3 is avoided either way: we never
   need a second channel. Recovering after promotion rather than before is what buys
   this.

3. **The delta reaches the other replicas for free.** Orchestrator repoints them to
   the successor during regroup, and with `log_replica_updates` on, the recovered
   transactions flow to them through normal replication.

4. **`targetGTIDSet` is a query, not stored state.** The agent can recompute it from
   `binlog.index` at any time, which is what allows two independent drivers to act on
   the same incident without a shared workflow record. `status` is a report.

5. **The same computation gives an audit trail on the unsafe path.** Because the
   target set is known before promotion, a bypassed failover can record precisely
   which transactions were lost — which is also exactly what tells the operator the
   old primary now holds errant transactions and must be rebuilt.

---

## 4. CRD and Interface Changes

### 4.1 CRD Spec Changes

- **`spec.unsafeFlags.failoverWithPossibleDataLoss`** *(optional, default `false`)*:
  When `true`, the operator does not wait for the delta: the successor is made
  writable as soon as Orchestrator promotes it. Fencing and salvage still run, so the
  loss is still measured and reported. Note that promotion still routes through the
  post-failover step rather than through Orchestrator directly (Section 5.6), so this
  is behaviourally equivalent to today but adds a small fixed delay. Only meaningful
  when `mysql.clusterType` is `async`.

  There is deliberately no `safeFailover.enabled` field. Safe failover is the
  behavior; the unsafe flag relaxes it, matching the existing `unsafeFlags`
  convention.

- **`spec.mysql.safeFailover.timeout`** *(optional, default `5m`)*: How long to wait
  for the delta to become available and be applied before `onTimeout` takes effect.
  Measured from the moment the incident is recorded.

- **`spec.mysql.safeFailover.onTimeout`** *(optional, default `Wait`)*: One of `Wait`
  or `Promote`. `Wait` keeps the cluster read-only indefinitely and requires a human
  decision — correct for users who prefer an outage to silent loss. `Promote` accepts
  the loss automatically once the timeout expires, recording it exactly as a manual
  bypass would.

- **`spec.backup.enabled` becomes effectively required for async clusters**, because
  the failover agent lives in the sidecar. This is enforced in the reconciler, not in
  CEL — see Section 5.10 — and relaxed by
  `spec.unsafeFlags.failoverWithPossibleDataLoss`.

### 4.2 CRD Status Changes

```yaml
status:
  failover:
    phase: CatchingUp
    failedPrimary: cluster1-mysql-0
    successor: cluster1-mysql-1
    startedAt: "2026-08-19T10:04:11Z"
    missingTransactions: 128
    message: "replaying delta from cluster1-mysql-0"
  lastFailover:
    completedAt: "2026-08-19T10:05:02Z"
    failedPrimary: cluster1-mysql-0
    successor: cluster1-mysql-1
    dataLoss: true
    lostGTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1041-1043"
    acceptedVia: Annotation
```

`phase` is one of `Fencing`, `Salvaging`, `WaitingForPrimaryData`, `CatchingUp`,
`Promoting`, `Blocked`. It is derived from observed reality on each reconcile, not
advanced by a workflow.

`lastFailover.lostGTIDSet` is empty on a successful safe failover. When non-empty it
is the incident record, and it drives the old primary's rebuild decision.
`acceptedVia` is one of `Annotation`, `UnsafeFlag`, `Timeout`.

Conditions:

- `SafeFailoverInProgress` — `True` while a failover is being recovered.
- `SafeFailoverBlocked` — `True` with reason `PrimaryDataUnavailable`,
  `BinlogsPurged`, `ConcurrentFailover`, `DurabilityNotGuaranteed`, or `Timeout`.

Events: `SafeFailoverStarted`, `PrimaryFenced`, `WaitingForPrimaryData` (repeated
while blocked, so monitoring notices), `DeltaRecovered`, `SafeFailoverCompleted`, and
`DataLossAccepted` (Warning).

### 4.3 Internal Contracts

**Bypass annotation on the `PerconaServerMySQL` CR:**

```
percona.com/force-failover: "cluster1-mysql-0"
```

The value must name the pod currently recorded as `status.failover.failedPrimary`. A
value that does not match is ignored with a warning event, so a forgotten annotation
cannot silently authorise a future failover. The operator clears the annotation once
it has been consumed.

**Sidecar HTTP API** (port 6450, alongside the existing `/backup/` and `/logs/`
routes):

| Endpoint | Purpose |
|---|---|
| `POST /failover/fence` | Write the fence marker; set `super_read_only`/`offline_mode` and kill client connections if `mysqld` is reachable |
| `DELETE /failover/fence` | Remove the marker |
| `POST /failover/source` | Start the binlog source on :33065; respond with the pod's complete binlog GTID set |
| `DELETE /failover/source` | Stop the source |

These are authenticated with the `operator` user's password as a bearer token, read
from the already-mounted creds volume. `/failover/fence` is a "take this pod out of
service" primitive and the port is reachable from anywhere on the pod network, so it
cannot stay unauthenticated the way `/backup/` is today.

**Fence marker:** `/var/lib/mysql/.failover-fence`, containing the incident
identifier. It is a local cache of a decision recorded in the CR, not the source of
truth — a pod rescheduled onto a new node has an unmarked volume, so the init
container consults `status.failover.failedPrimary` before `mysqld` starts and writes
the marker itself.

**Sidecar container additions:** the TLS secret volume mount (absent from
`backupVolumeMounts` today) and container port 33065.

**Orchestrator configuration**, static in the ConfigMap regardless of any spec flag,
and added to `reservedOrchestratorConfigKeys`:

| Key | Value | Why |
|---|---|---|
| `ApplyMySQLPromotionAfterMasterFailover` | `false` | The successor must stay read-only until the delta is applied. This is the gate. |
| `RecoverNonWriteableMaster` | `false` | Currently `true`; Orchestrator would classify our deliberately read-only primary as a fault and make it writable, defeating the feature. |
| `PreFailoverProcesses` | fence + salvage | New. Also fires on graceful takeover, so the hook branches on `{command}`. |
| `PreGracefulTakeoverProcesses` | fence for switchover | New. Currently unused; runs with `failOnError`, so it can abort a switchover. |
| `OnFailureDetectionProcesses` | record incident | Changed from an echo. |

**Readiness:** `cmd/healthcheck` must fail readiness when the fence marker is present.
Today `sleep-forever` and `no-bootstrap` cause it to exit 0, reporting the pod Ready
(`cmd/healthcheck/main.go:31-37`).

### 4.4 User-Facing Behavior Changes

- After a primary failure, writes stay unavailable slightly longer than today — for as
  long as the delta takes to apply — and then resume with no loss. Clients see
  connection failures during the window, not stale reads and not accepted writes.
- `kubectl get ps` and the CR status show what the cluster is waiting for and what the
  user's options are, rather than the failover appearing instantaneous and complete.
- A failover that lost data says so, permanently, in `status.lastFailover`.

---

## 5. Design Decisions and Alternatives

### 5.1 Gate by holding the successor read-only after promotion

**Chosen approach:** Let Orchestrator select and promote the successor as it does
today, but configure it to leave the successor `super_read_only=1`. Apply the delta
afterwards, then make it writable.

**Why:** Constraint 2.2.2 forces a choice between a gate that can abort but does not
know the successor, and a point that knows the successor but cannot abort. Choosing
the latter looks weaker until you notice that the gate does not have to be the hook at
all. The successor's own `super_read_only` is the gate, and it is enforced by database
state rather than by a process staying alive. A killed hook, a rebooted node, or a
restarted operator all leave the cluster read-only and recoverable rather than
writable and wrong.

It also avoids Constraint 2.2.3 entirely. After promotion Orchestrator has already
detached the successor's replication channel, so the catch-up uses a single ordinary
channel. The pre-failover alternative would have to attach a second channel to an
instance Orchestrator is actively polling.

**Alternatives considered:**

| Alternative | Why Rejected |
|---|---|
| Recover in `PreFailoverProcesses`, before promotion | Requires us to select and pin the candidate via Orchestrator's `register-candidate` API, duplicating logic Orchestrator already does better (lag-aware, most-advanced). Requires a second replication channel on an instance under active polling (Constraint 2.2.3). And the gate becomes a process exit code: if the hook dies, the failover simply proceeds. |
| Operator-driven failover with `RecoverMasterClusterFilters: []` | Gives total control but discards Orchestrator's raft-corroborated detection, or forces us to reimplement it. Contradicts the premise that the failover decision is Orchestrator's. Large behavioral change for a marginal gain. |
| Recover after the successor is already writable | Not zero loss in any meaningful sense. Injecting old transactions after the application has read and written on top of the new state breaks causality even though no GTID is lost. |

### 5.2 Replay the delta over a replication channel

**Chosen approach:** The successor pulls the delta from a binlog source using an
ordinary `SOURCE_AUTO_POSITION=1` channel.

**Why:** The replication applier is not subject to `super_read_only`, so the successor
can stay fenced against clients while it catches up — which is the stated requirement
and also the right behavior. GTID auto-position makes MySQL itself compute the delta,
so the correctness of the delta calculation is MySQL's problem, not ours. And the
transactions arrive by exactly the mechanism that would have delivered them had the
primary lived, which keeps DDL, session context, and binlog re-emission identical.

**Alternatives considered:**

| Alternative | Why Rejected |
|---|---|
| `mysqlbinlog --exclude-gtids ... \| mysql` into the successor | Requires `super_read_only=0` during the apply, violating the requirement and opening a real window in which the successor is writable but incomplete. Single-threaded client-side apply. Reuses PITR-shaped code, which is its only advantage. |
| Relay-log injection (the MHA technique): copy binlogs onto the successor as relay logs and start only the SQL thread | Bypasses `super_read_only` and needs no source process, which is genuinely attractive. Rejected as version-fragile: `RELAY_LOG_FILE` positioning interacts awkwardly with GTID auto-position and `relay_log_recovery` in 8.0/8.4, and it writes files into the successor's data directory. Worth revisiting if the source turns out to be the weak point. |

### 5.3 A go-mysql binlog source in the sidecar

**Chosen approach:** Implement the source with
`github.com/go-mysql-org/go-mysql`'s `server` package, running inside the sidecar
container and streaming raw events from the failed primary's binlog files.

**Why:** The library already provides the server half of replication:
`server.ReplicationHandler` hands us `HandleBinlogDumpGTID(gtidSet *mysql.MysqlGTIDSet)`
with the replica's GTID set **already parsed**, and `Conn.writeBinlogEvents` already
frames events back onto the wire. Handshake, TLS, and `caching_sha2_password` are
provided — the last mattering because of Constraint 2.2.9. `replication.BinlogParser`
in raw mode reads the files. What remains is a table of canned answers to the replica
IO thread's setup queries, start-file selection by `PREVIOUS_GTIDS`, and rotate and
heartbeat events. A few hundred lines with unit-testable pure functions, not a
protocol project.

Running in the sidecar means it is alive precisely when `mysqld` is not, needs no data
directory, no scratch space on the PVC, and starts in milliseconds — which is what
keeps the two-minute budget comfortable.

**Alternatives considered:**

| Alternative | Why Rejected |
|---|---|
| `mysqld` with a freshly initialized scratch datadir and the salvaged binlogs hardlinked into its index | No new protocol code, stock semantics. But it must run in the `mysqld` container, which is crash-looping — and kubelet's CrashLoopBackOff reaches five minutes, forcing the operator to delete the pod to get a prompt restart. Plus 10–20s of `--initialize` and a few hundred MB of PVC scratch. Deterministic latency mattered more than familiarity. |
| A dedicated always-present sidecar container running the operator image | Avoids coupling to `spec.backup.enabled`, but adds a container to every MySQL pod forever, with its own resource requests, security context, and documentation. |
| An on-demand recovery pod mounting the old primary's PVC | Zero steady-state cost, but puts pod scheduling — image pull, node capacity, taints — into the failover critical path, and races the StatefulSet for the ReadWriteOnce volume when the pod is recreated after node loss. |
| Percona's existing `percona-binlog-server` (used for PITR) | It is a pull client that streams binlogs to S3; it does not serve the replication protocol. A serve mode would be a cross-team dependency. Worth raising with its owners as a longer-term consolidation. |

### 5.4 Reuse the xtrabackup sidecar; require backups on async

**Chosen approach:** Put the agent in the existing sidecar and treat
`spec.backup.enabled: true` as required for async clusters, relaxed by the unsafe
flag.

**Why:** `backup.enabled: true` is already the default (`deploy/cr.yaml:694`), so
nearly every async cluster already runs the sidecar. It already mounts
`/var/lib/mysql` and the users secret and already serves HTTP. Adding a second
container to every pod to avoid a coupling that most users never encounter is a worse
trade.

The cost is honest and should be documented: a user who turns backups off on an async
cluster loses safe failover, and the operator tells them so through a status
condition.

### 5.5 State derived from reality, with two independent drivers

**Chosen approach:** The sequence lives in `pkg/failover` and is invoked by both
`orc-handler` (from hooks) and the operator's reconcile loop. Neither is required for
the other to work. Phase is computed from observed state — is there a fence marker, is
the primary read-only, does the successor's `gtid_executed` cover the target — rather
than stored and advanced.

**Why:** Constraint 2.2.7. Making the operator a required participant in every
promotion would mean an operator outage becomes a MySQL write outage, which is worse
than the problem being solved. Two drivers require that they cannot corrupt each
other, and the cheapest way to get that is to have no shared mutable workflow record:
`targetGTIDSet` is recomputable from `binlog.index` on demand, every step is
idempotent, and `status` is a report rather than a state machine.

**Alternatives considered:**

| Alternative | Why Rejected |
|---|---|
| Operator owns the state machine; hooks are thin clients | Simplest to reason about, and it was the first shape of this design. Rejected on Constraint 2.2.7. |
| `orc-handler` owns everything; operator uninvolved | No backstop when the hook process dies mid-recovery, and no home for timeout and bypass policy, which are reconcile-shaped concerns. |

### 5.6 Static Orchestrator configuration; all policy in the operator

**Chosen approach:** `ApplyMySQLPromotionAfterMasterFailover: false` and
`RecoverNonWriteableMaster: false` are written unconditionally, not derived from
`spec.unsafeFlags.failoverWithPossibleDataLoss`. The flag changes only what the
operator does after promotion.

**Why:** Deriving Orchestrator config from a spec flag means flipping the flag
rewrites the ConfigMap and restarts Orchestrator. A user reaching for the flag is
almost always mid-incident, and restarting Orchestrator during an active recovery is
the worst possible moment. Keeping the config static means the escape hatch never
touches Orchestrator. It is also why the one-shot path is an annotation rather than a
spec field.

### 5.7 Bypass: a standing flag and a one-shot annotation

**Chosen approach:** Both. `spec.unsafeFlags.failoverWithPossibleDataLoss` for users
who never want to wait, and `percona.com/force-failover: "<failed-primary>"` for the
incident escape hatch.

**Why:** They serve different moments. The flag is a policy decision made calmly in
advance; the annotation is a decision made under pressure about one specific incident.
Collapsing them into one field would either force a config change mid-incident or make
a per-incident decision permanent. Requiring the annotation to name the failed primary
means a forgotten annotation cannot authorise a future failover, which is the failure
mode that makes one-shot overrides dangerous.

### 5.8 Fence at `PreFailoverProcesses`, not at detection

**Chosen approach:** `OnFailureDetectionProcesses` only records the incident. Fencing
and starting the source happen in `PreFailoverProcesses`.

**Why:** Fencing at detection would overlap our work with Orchestrator's
deliberation, which matters only if that work is slow. With the go-mysql agent it is
not — no data directory, no process spawn. Fencing at detection would buy a couple of
seconds and introduce a whole failure class: detection can be a false positive, and
fencing a healthy primary means we caused the outage, which then needs a fence lease
and an un-fence path. Moving the fence behind Orchestrator's decision deletes that
class.

It also turns `PreFailoverProcesses`'s abort semantics into a safety property: if a
pod is present and we cannot fence it, aborting the failover is the right answer,
because promoting away from an unfenced primary risks split-brain. A pod that is
absent is not a fence failure — absence is fencing.

Aborting is cheap to repeat but not free to observe. `RecoveryPeriodBlockSeconds: 5`
in our configuration means Orchestrator retries the analysis every few seconds, so
every step behind this hook must be idempotent, and event emission must be
rate-limited so a persistently blocked failover does not flood the event stream.

Finally, this hook fires on planned switchovers as well, because a graceful takeover
is implemented as a forced `DeadMaster` recovery. The hook branches on `{command}` and
does nothing on the graceful path — see Section 6.2.

### 5.10 Reconciler validation, not CEL, for the backup requirement

**Chosen approach:** The operator validates the sidecar's availability during
reconcile and raises a status condition; it does not add a CEL rule.

**Why:** Constraint 2.2.8. A CEL rule such as
`!(clusterType == 'async') || unsafeFlags.X || backup.enabled` would, the moment the
operator is upgraded, hard-block every update to any existing async cluster that has
backups disabled — including the update that would fix it. A status condition
degrades the feature and explains why, which is the correct blast radius for a new
requirement on existing clusters.

---

## 6. Replication Model Impact

### 6.1 Group Replication Behavior

Unaffected. GR promotion runs through Group Replication's own consensus and
`mysqlsh`, never through Orchestrator's recovery hooks. The new Orchestrator
configuration keys are only written for async clusters, and the sidecar's failover
endpoints are never invoked.

### 6.2 Async Replication Behavior

This is the feature. Both the unplanned failover path (Section 3.2) and the planned
switchover path apply.

**Switchover** is the degenerate case of the same machinery, and the operator already
performs it: `smartUpdate` → `switchOverAndWait` → `switchOverAsync` →
`orchestrator.EnsureNodeIsPrimary`, which calls `graceful-master-takeover-auto`
(`pkg/controller/ps/upgrade.go:241`). Because the old primary is alive, there is no
salvage and no binlog source: the target GTID set is simply its `@@GLOBAL.gtid_executed`
read *after* it has been fenced, and the candidate catches up from the real primary on
the channel it already has.

Orchestrator's graceful takeover is already close to correct, and the design should
not pretend otherwise: it runs `PreGracefulTakeoverProcesses`, sets the old master
`super_read_only`, and then calls `WaitForExecBinlogCoordinatesToReach` so the
designated replica must reach the frozen master's exact binlog coordinates before the
promotion proceeds — returning an error rather than promoting if it cannot. It then
promotes through the same `recoverDeadMaster` path, which applies `RESET SLAVE ALL`
and `read_only=0` regardless of `ApplyMySQLPromotionAfterMasterFailover` because the
graceful `CommandHint` short-circuits that check.

Three things change, and they are deliberately small:

1. `PreGracefulTakeoverProcesses` fences at the Kubernetes level — failing the old
   primary's readiness so it leaves Service endpoints — which Orchestrator does not
   do. It aborts the switchover if that fails.
2. `switchOverAndWait` asserts `GTID_SUBSET` after the takeover returns, converting
   Orchestrator's coordinate-based wait into an explicit GTID assertion in our own
   terms.
3. The `PreFailoverProcesses` hook must recognise `{command}` as
   `graceful-master-takeover` and skip fencing and salvage entirely — otherwise every
   planned switchover would fence a healthy primary and start a binlog source it does
   not need.

There is no read-only hold on this path and none is needed: the old primary is alive,
so Orchestrator's wait already establishes the contract before promotion. The
guarantee here is unconditional.

### 6.3 Differences and Why

Async needs this and GR does not because async acknowledges a commit as soon as the
primary's own binary log is fsynced, with no replica involvement. GR does not commit
until a majority of the group has certified the transaction, so a surviving majority
already holds every acknowledged write and promotion is lossless by construction.

---

## 7. User Experience

### 7.1 Existing CR (Unchanged)

```yaml
apiVersion: ps.percona.com/v1
kind: PerconaServerMySQL
metadata:
  name: cluster1
spec:
  mysql:
    clusterType: async
    size: 3
  orchestrator:
    enabled: true
  backup:
    enabled: true
```

Safe failover is active with no configuration. Failovers take longer and lose nothing.

### 7.2 Availability-first: bound the wait

```yaml
spec:
  mysql:
    clusterType: async
    safeFailover:
      timeout: 2m
      onTimeout: Promote   # accept loss automatically after 2m, and record it
```

### 7.3 Opting out entirely

```yaml
spec:
  unsafeFlags:
    failoverWithPossibleDataLoss: true   # promote immediately, accept loss
```

Fencing and salvage still run, so `status.lastFailover.lostGTIDSet` still reports what
was lost — the user gives up the wait, not the visibility.

### 7.4 Bypassing during an incident

The cluster is stuck because the old primary's node is gone:

```
$ kubectl get ps cluster1 -o jsonpath='{.status.failover}'
{"phase":"WaitingForPrimaryData","failedPrimary":"cluster1-mysql-0",
 "successor":"cluster1-mysql-1","message":"cluster1-mysql-0 is not running; its
 binary logs are required to fail over without data loss"}
```

Three options, in the order the operator recommends them:

```bash
# 1. Wait — the pod may return on its own.

# 2. Tell Kubernetes the node is really gone. This force-detaches the volume so the
#    StatefulSet can reschedule the pod with its PVC, and safe failover proceeds.
kubectl taint node <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute

# 3. Accept the loss for this incident only.
kubectl annotate ps cluster1 percona.com/force-failover=cluster1-mysql-0
```

After option 3:

```yaml
status:
  lastFailover:
    dataLoss: true
    lostGTIDSet: "3e11fa47-71ca-11e1-9e33-c80aa9429562:1041-1043"
    acceptedVia: Annotation
```

---

## 8. Error Handling and Edge Cases

### 8.1 The old primary's pod is not running

**Scenario:** Node loss, or the pod is otherwise absent, so nothing can read its
binary logs.

**Expected behavior:** Phase `WaitingForPrimaryData`, condition `SafeFailoverBlocked`
with reason `PrimaryDataUnavailable`. The successor stays read-only. A warning event
repeats while blocked, so this is visible to monitoring rather than only to anyone
reading the CR. The event names the three options from Section 7.4. Requeue on a short
interval; apply `onTimeout` when the timeout expires. The operator never force-deletes
the pod — see Section 1.2.

### 8.2 The needed binary logs have been purged

**Scenario:** The transactions the successor is missing are no longer in any file the
agent can read.

**Expected behavior:** Detected before streaming starts, by comparing the oldest
available file's `PREVIOUS_GTIDS` against the successor's `gtid_executed`. Condition
`SafeFailoverBlocked` with reason `BinlogsPurged`, immediately. Waiting cannot help,
so the operator says so instead of consuming the timeout and then failing with a bare
`Got fatal error 1236` in the replica's error log. Only a bypass can move the cluster
forward.

### 8.3 The old primary restarts during recovery

**Scenario:** The pod comes back — same node or rescheduled — while the failover is in
progress.

**Expected behavior:** It must not start `mysqld` normally and must not be
re-discovered as a primary. The fence marker on its volume handles the same-node case.
For a rescheduled pod the volume is unmarked, so the init container consults
`status.failover.failedPrimary` before `mysqld` starts and writes the marker itself.
The init container blocks on that check; `mysqld` never races it. The Orchestrator
downtime set during fencing prevents re-attachment independently.

### 8.4 The successor cannot reach the binlog source

**Scenario:** TLS failure, authentication failure, or a network problem between the
successor and the fenced pod.

**Expected behavior:** Retry with backoff, surfacing the replica's error in
`status.failover.message`. The read-only hold means no unsafe outcome is possible
while this persists. `onTimeout` applies as usual.

### 8.5 A second failover during recovery

**Scenario:** The successor dies while it is catching up.

**Constraint:** One safe failover in flight. A second detection while
`status.failover.phase` is not empty results in `SafeFailoverBlocked` with reason
`ConcurrentFailover` and requires human resolution.

**Rationale:** Two overlapping incidents mean two fenced pods and two target sets, and
the resulting merge is not something to get right on the first release. Documented as
a v1 limitation rather than handled badly.

### 8.6 Durability settings do not support the guarantee

**Scenario:** A user sets `sync_binlog=0`, or `log_replica_updates` is off.

**Expected behavior:** With `sync_binlog != 1` the binary log is no longer a record of
acknowledged commits and the central correctness argument (Section 2.1) does not hold;
the operator raises `SafeFailoverBlocked` with reason `DurabilityNotGuaranteed` and
says the guarantee is void. With `log_replica_updates` off the recovered delta would
not propagate from the new primary to the other replicas or to the PITR binlog server;
the same condition is raised.

### 8.7 The old primary rejoins after the failover

**Scenario:** The fenced pod returns once the failover has completed.

**Expected behavior:** After a *safe* failover its GTID set is a subset of the new
primary's, so it re-attaches as an ordinary replica with no rebuild. After a *lossy*
failover it holds transactions the new primary never received — errant transactions —
and must be rebuilt. The operator compares its `gtid_executed` against the new
primary's and, if it is not a subset, forces a clone through the existing `clone.lock`
mechanism in `cmd/bootstrap/async` before removing the fence.

### 8.8 Orchestrator decides not to fail over

**Scenario:** Detection fires but Orchestrator declines to recover.

**Expected behavior:** Nothing has happened. `OnFailureDetectionProcesses` only
records the incident, and fencing lives behind the decision (Section 5.9), so there is
no fenced pod to release and no un-fence path to get wrong.

### 8.9 PITR binlog server across a failover

**Scenario:** `spec.backup.pitr` is enabled when the primary fails.

**Expected behavior:** The new primary re-emits the recovered delta into its own
binary log, and the binlog server resumes by GTID (`ReplicationModeGTID` in
`pkg/binlogserver/config.go`), so the S3 stream has no gap. No code change; it needs a
test.

### 8.10 Orchestrator detaches the catch-up channel

**Scenario:** `MasterFailoverDetachReplicaMasterHost` runs as a postponed function,
after every `Post*` hook. Normally it is harmless — by then we have issued
`RESET REPLICA ALL`, the successor is no longer a replica, and
`DetachReplicaMasterHost` refuses with "instance is not a replica". But if our hook
exits early — killed, or hitting its own deadline while the operator backstop
continues the catch-up asynchronously — the postponed detach can fire *while* the
successor is replicating from the binlog source. It issues `STOP REPLICATION` and
repoints the channel at a mangled host, silently stalling the recovery.

**Expected behavior:** The backstop must treat the channel as something to converge
on, not something it configured once: on each pass, if the successor is not caught up
and its channel is not pointing at the binlog source and running, reconfigure it. This
falls out of deriving state from reality (Section 5.5) rather than tracking it, but it
is the concrete reason that choice matters and must be covered by a test.

---

## 9. Migration and Backward Compatibility

### 9.1 Existing Clusters

Existing async CRs get safe failover after an operator upgrade with no spec change,
which is the intended behavior and the point of the ticket. The observable difference
is that a failover takes longer and does not lose data.

Async clusters running with `spec.backup.enabled: false` do **not** silently lose
protection: they get `SafeFailoverBlocked` with a message explaining that the sidecar
is required, and they behave exactly as they do today. Enabling backups or setting
`spec.unsafeFlags.failoverWithPossibleDataLoss: true` resolves the condition. Nothing
about their CR becomes un-updatable (Section 5.10).

Group Replication clusters are untouched.

### 9.2 CRD Compatibility

All changes are additive optional fields: `spec.mysql.safeFailover` (with
`timeout` and `onTimeout`), `spec.unsafeFlags.failoverWithPossibleDataLoss`, and the
new `status.failover` / `status.lastFailover` sections. No CEL rules are added.
Requires `make generate` and `make manifests`.

The pod template changes — a TLS mount and a container port on the sidecar — trigger
one rolling restart of the MySQL StatefulSet on upgrade, as any pod spec change does.
Gate them behind `cr.CompareVersion(...)` following the existing convention.

### 9.3 Operator Version Skew

During the operator's own rolling upgrade, the Orchestrator ConfigMap may already
carry `ApplyMySQLPromotionAfterMasterFailover: false` while some MySQL pods still lack
the failover agent. If a failover happens in that window, the successor is promoted
read-only and the fast path fails to reach an agent. The operator's reconcile backstop
detects the missing agent and reports `PrimaryDataUnavailable`, and the standard
bypass restores write availability. The window is small but real and should be called
out in the release notes.

---

## 10. Testing Strategy

The acceptance criterion must be stronger than "the cluster came back". The core test
records **client-acknowledged** commits and asserts every one of them survives.

### 10.1 E2E Test Scenarios

| Scenario | Cluster Type | What It Validates |
|---|---|---|
| Write in a loop recording acked commits, stop a replica's IO thread to manufacture a real delta, SIGKILL the primary | Async | The zero-loss claim, directly: the new primary contains every acked row |
| Same, but delete the node; then bypass with the annotation | Async | `WaitingForPrimaryData`, accuracy of `lostGTIDSet`, and that the old primary is rebuilt when it returns |
| Purge binary logs past the successor's position, then fail over | Async | `Blocked/BinlogsPurged` raised immediately, not after the timeout |
| `onTimeout: Promote` with an unreachable old primary | Async | Automatic promotion at the deadline, loss recorded with `acceptedVia: Timeout` |
| Smart update that switches the primary over | Async | Zero loss on the planned path, and no unplanned failover is triggered |
| `unsafeFlags.failoverWithPossibleDataLoss: true` | Async | Promotion is immediate; loss still measured and reported; total failover time stays within today's envelope |
| `backup.enabled: false` on async | Async | Condition raised, safe failover disabled, cluster otherwise healthy and updatable |
| PITR enabled across a failover | Async | Binlog server resumes by GTID; no gap in S3 |
| Kill the hook mid-catch-up and let the operator finish | Async | The postponed `DetachReplicaMasterHost` cannot strand the recovery (Section 8.10) |
| Trigger a graceful switchover with the failover hooks installed | Async | `PreFailoverProcesses` no-ops on `{command}`: no fencing and no binlog source on a healthy primary |
| Existing GR failover suite | GR | No regression; GR path untouched |

### 10.2 Unit and Integration Coverage

Pure functions, table-driven: `PREVIOUS_GTIDS` extraction, start-file selection
against a replica's GTID set, torn-tail truncation, GTID subset arithmetic, phase
derivation from observed state.

One integration test running the go-mysql binlog source against a real PS 8.4 replica,
covering the assumptions in Section 11 items 1–3. This test is the first thing to
write, before any production code.

---

## 11. Open Questions

1. **Replica-side GTID skip.** The source streams from the selected file without
   filtering, relying on the replica to ignore transactions already in its
   `gtid_executed`. *Resolution: assumed true, deliberately, on 2026-08-19. Flagged
   here because it is load-bearing — if it is false, transactions could be applied
   twice. The integration test in Section 10.2 must assert it explicitly. Adding
   source-side filtering is cheap (go-mysql hands us the parsed GTID set) and is the
   fallback.*

2. **`caching_sha2_password` from a PS 8.4 replica IO thread to a go-mysql server over
   the cluster's TLS.** Constraint 2.2.9 rules out `mysql_native_password`. Must be
   verified before implementation.

3. **Raw event pass-through fidelity** with `binlog_checksum` negotiation and
   `binlog_transaction_compression` in use.

4. **Does Orchestrator's graceful-takeover path apply promotion regardless of
   `ApplyMySQLPromotionAfterMasterFailover`?** *Resolved 2026-08-19 against the
   Percona fork at `820293a`: yes. `recoverDeadMaster` applies the promotion when
   either the flag is set or the analysis carries the graceful `CommandHint`, so a
   switchover always ends with the new primary writable. This turned out to be
   harmless: the graceful path fences the old master and waits for the designated
   replica to reach its exact binlog coordinates before promoting, so the contract is
   already established. Section 6.2 was rewritten accordingly, and the read-only hold
   is failover-only.*

5. **Does a non-zero exit from `PreFailoverProcesses` abort the recovery in the
   Percona fork, and at what cadence does it retry?** *Resolved 2026-08-19: yes.
   `executeProcesses` runs with `failOnError`, stops at the first non-zero exit, and
   `recoverDeadMaster` returns `recoveryAttempted=false`. Two consequences. Hooks have
   no timeout at all, so ours must enforce `safeFailover.timeout` itself or it will
   block the recovery forever. And our `orchestrator.conf.json` sets
   `RecoveryPeriodBlockSeconds: 5` against an upstream default of 3600, so an aborted
   recovery is retried roughly every five seconds — every step must be idempotent and
   cheap to repeat, and the operator should rate-limit the events it emits so a
   blocked failover does not flood the event stream.*

6. **`github.com/go-mysql-org/go-mysql` as a direct dependency.** It is
   MIT-licensed, with Vitess-derived code under a separate BSD-3-clause Google
   license kept in-tree as `vitess_license`. Needs Percona's dependency review before
   it lands.

7. **Sidecar endpoint authentication.** The bearer-token-from-creds-volume proposal in
   Section 4.3 is the minimum. Confirm whether it should also cover the existing
   `/backup/` and `/logs/` routes, which are unauthenticated today — arguably a
   pre-existing issue this work should not silently inherit.

8. **HAProxy check interval.** Constraint 2.2.10 costs up to ten seconds of the
   two-minute budget. Decide whether the `mysql-primary` backend warrants a shorter
   `inter` now or as a follow-up.

---

## Appendix

### A. Glossary

| Term | Definition |
|------|------------|
| Async | Asynchronous replication: the primary acknowledges a commit without waiting for any replica |
| GTID | Global Transaction Identifier; uniquely names a transaction across the topology |
| Auto-position | `SOURCE_AUTO_POSITION=1`; the replica sends its `gtid_executed` and the source computes the delta |
| Delta | Transactions present in the failed primary's binary log but absent from the successor |
| Fencing | Rendering a node unable to accept writes and invisible to clients and to the topology manager |
| Errant transaction | A transaction on one instance that the current primary does not have |
| PiTR | Point-in-Time Recovery via binary log replay |
| Successor / candidate | The replica Orchestrator promotes to primary |

### B. References

- [Orchestrator: configuration, recovery hooks](https://github.com/openark/orchestrator/blob/master/docs/configuration-recovery.md)
- [MySQL: replication with GTIDs](https://dev.mysql.com/doc/refman/8.4/en/replication-gtids.html)
- [MySQL: binary logging options and durability](https://dev.mysql.com/doc/refman/8.4/en/replication-options-binary-log.html#sysvar_sync_binlog)
- [Kubernetes: non-graceful node shutdown](https://kubernetes.io/docs/concepts/architecture/nodes/#non-graceful-node-shutdown)
- [go-mysql](https://github.com/go-mysql-org/go-mysql)
