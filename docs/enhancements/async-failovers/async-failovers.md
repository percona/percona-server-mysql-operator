# K8SPS-842: Async failovers

The goal is to ensure failovers cause **zero data loss**. The architecture is designed with the preference of durability over availability.

An async cluster under load will always have a certain replication lag between the primary and replicas. Replication lag, in most cases, means the transaction is transmitted to the replica and is in replica's relay log. Whatever transaction replica has in its relay log will be durable and replica will eventually apply it. However the primary might have been killed (due to OOM or hardware failure) before transmitting the transaction to replicas. In this case the transaction only exists in the binary logs of the primary. Achieving zero data loss needs to take this scenario into consideration. The architecture we're putting together always assumes there's a transaction that only exists in failed primary's binary log. We always try to salvage it.

This architecture has 3 pillars:
1. Salvaging binary logs from failed primary
2. Orchestrating failover via Orchestrator's hooks
3. Overseeing the recovery via Operator

## Salvaging binary logs from failed primary

The salvage operation (`cmd/failover`) performs these high-level steps on the failover target:
1. Probe the failed primary and stand down if it's back (see below)
2. Stop replication (`STOP REPLICA`)
3. Flush relay logs (`FLUSH RELAY LOGS`)
4. Read the replica's positions (`Source_Log_File`, `Read_Source_Log_Pos`, `Relay_Log_File`)
5. Fetch the missing binary logs from the failed primary
6. Truncate a torn tail off the last fetched binary log
7. Splice the fetched binary logs into the latest closed relay log
8. Start the applier (`START REPLICA SQL_THREAD`)
9. Wait until the applier works through the spliced events

On success IO_THREAD stays stopped: the replica is about to be promoted, and there's no source left to receive from.

### Fetching binary logs from failed primary

All MySQL pods have a xtrabackup sidecar that runs an HTTP server to control backups. We're adding a new endpoint to this server, `POST /failover/stream` on port 6450, to stream binary logs to the client. Requests must use HTTP basic auth as the `operator` user. The sidecar reads the password from the secret on every request, so a rotated password is picked up, and it rejects every request if the password is empty. The server streams binary logs starting from the log file and position the client requests.

Server expects a starting binary log and position. It's the client's responsibility to request what it doesn't have. This decision is made by checking `Source_Log_File` and `Read_Source_Log_Pos`. These together report the latest position that replica's IO_THREAD received from the primary.

For example, if the failed primary has the following in its `binlog.index`:
```
./binlog.000001
./binlog.000002
./binlog.000003
./binlog.000004
./binlog.000005
./binlog.000006
./binlog.000007
./binlog.000008
```

Client can send a request like:
```
{"binary_log": "binlog.000004", "position": 1789}
```

Server will serve `binlog.000004` starting from position 1789, and logs from `binlog.000005` to `binlog.000008` fully. All these files are served as a single tar stream. It's the client's responsibility to extract tar and put files in the correct places. Client (`cmd/failover`) receives the tar stream from the server and extracts it to the staging dir (`failover -staging-dir`, default `/var/lib/mysql/source-logs`).

### What if the old primary's pod is unreachable?

Failover requires old primary's pod to be reachable for fetching the binary logs. It doesn't require failed primary's `mysqld` to be up and running, but its sidecar should be able to serve HTTP traffic.

If the old primary's pod can't be scheduled or reached over the network, the attempt fails and orchestrator retries it later. Each attempt has two bounds:
1. `cmd/orc-handler` waits up to 2 minutes for the source pod to exist and get an IP (`sourcePodWait`).
2. `cmd/failover` gives up once the source sends nothing for 2 minutes (`-stall-timeout`). This covers the sidecar not listening yet, the response headers and every read of the body. A transfer that keeps making progress is never cut by it.

The failover timeout (default `6h`) is not per attempt. It spans all of orchestrator's retries and is counted from the first attempt for this source. `cmd/orc-handler` records that moment in a `seen` mark (`orc-handler/seen/<source>`) and passes each attempt only what's left of the budget as `failover -timeout`. Once the budget is spent, the `onTimeout` policy applies. A mark that no attempt refreshed for an hour is dropped, so an unrelated later failure of the same source starts with the full budget.

### Updating replica relay logs

Failover binary does the following to update relay logs:
1. Stop replication (stop IO and SQL threads)
2. Flush relay logs
3. Splice fetched binary logs into the latest closed relay log

Imagine the following `SHOW REPLICA STATUS` output:
```
     Replica_IO_State:
            Source_Host: cluster1-mysql-0.cluster1-mysql.ps-6688
            Source_User: replication
            Source_Port: 3306
          Connect_Retry: 60
        Source_Log_File: binlog.000002
    Read_Source_Log_Pos: 142007
         Relay_Log_File: cluster1-mysql-1-relay-bin.000002
          Relay_Log_Pos: 142218
  Relay_Source_Log_File: binlog.000002
```

Active relay log is `cluster1-mysql-1-relay-bin.000002` right now.

Failover first runs `STOP REPLICA` to stop threads and then `FLUSH RELAY LOGS`. Once relay logs are flushed, `mysqld` creates a new relay log: `cluster1-mysql-1-relay-bin.000003`. Since replication threads are not running, the last read relay log is still `000002`.

Then we splice binary logs into `000002` and run `START REPLICA SQL_THREAD`. SQL thread continues reading from `cluster1-mysql-1-relay-bin.000002` and starts applying new events we appended to it. For each binary log except the first, we also append an artificial rotate event into relay log so `Relay_Source_Log_File` is updated accordingly. `Relay_Source_Log_File` is important for orchestrator to decide the suitable primary for promotion.

### What if the old primary comes back and becomes ready to serve while replica applies relay logs?

Failover binary starts probing old primary's mysqld (using `Source_Host`) before touching anything on the replica. This probe does the following:

1. Connect to `Source_Host` using operator user and ping
2. Ensure it's a primary. A writable source is one, whatever channel it carries: a promoted primary can keep the detached `//host` channel orchestrator leaves when `RESET SLAVE ALL` fails, and failing over past it would leave two writers. A restarted pod always comes back read-only, and only orchestrator makes it writable again, so the probe doesn't wait for that. A read-only source is a primary only if it has no replication channel of its own. One that bootstrapped as a replica, for example of the candidate after a GTID tie, is not a primary coming back: handing the candidate to it would make the two replicate from each other.
3. Ensure replica doesn't hold any transaction that the source lost. If it does, we should continue failover.

The probe polls every 5 seconds, and it takes two consecutive successes to abort the failover. It runs at three points:
1. **Before touching the replica:** `cmd/failover` probes the source first. A source that's already back costs only the probe.
2. **During the drain:** while waiting for the applier, `cmd/failover` keeps probing in the background (`watchSource`). A confirmed source wins over a finished drain, because promoting while the source serves again would leave two writable primaries.
3. **Before registering the candidate:** after the worker succeeds, `cmd/orc-handler` runs `failover -probe` on the candidate again, bounded to 1 minute, and only then calls `RegisterCandidate`. This covers a source that came back between the end of the drain and the promotion. The force path (`onTimeout: ForceWithPossibleDataLoss`) runs the same probe before it promotes. The probe is skipped if the source pod doesn't exist, since a source without a pod can't be back.

#### Standing down

When the probe confirms the source is back, the worker hands the replica back instead of promoting it:
1. It runs `START REPLICA IO_THREAD`. On the initial probe this happens only if the receiver isn't already running, for example because an earlier attempt stopped it and then failed.
2. It keeps the splice. The applier keeps working through the spliced events from the relay log while the receiver re-requests the overlapping range from the source. GTID auto-position skips whatever has already been executed by the time it arrives, so nothing is applied twice.
3. It waits up to 20 seconds for the receiver to reconnect, and only logs if it doesn't.
4. It prints `FAILOVER-RESULT: source-recovered` to stdout and exits non-zero.

`cmd/orc-handler` finds the marker in the worker's output. The hook still fails, so orchestrator abandons the recovery and doesn't promote anything, but the hook also:
- clears the source's `seen` mark, so the next failure of this source gets the full budget;
- mutes the `FailoverFailed` event for this source for the dedup interval, because the cluster has its primary back and there's nothing to warn about.

Nothing in the hook makes the source writable. Once the recovery is abandoned and acknowledged, the source is alive, so there's no `DeadMaster` analysis left to promote anything. With the candidate's receiver reconnected, orchestrator sees a read-only master with healthy replicas (`NoWriteableMasterStructureWarning`), and `RecoverNonWriteableMaster` makes it writable under its own recovery registration. The operator then moves the primary label back and acknowledges that recovery through the [stale recovery backstop](#stale-recovery-backstop). If the source comes back after the last probe, the candidate is promoted instead. The source stays read-only, because our recovery is still active and nothing else may register one.

`RecoverNonWriteableMaster` needs a replica of the source with both its IO and SQL threads running. If none gets there, for example in a two-pod cluster whose candidate's applier stopped on the splice or whose receiver never reconnected, the source stays read-only and the cluster has no writable primary. A force-promote is refused while orchestrator can reach it. The way out is to fix the replica's replication (`SHOW REPLICA STATUS` names the error), after which orchestrator makes the source writable on its own.

## Orchestrating the failover via Orchestrator's hooks

Orchestrator runs the `cmd/orc-handler` binary for failover in `PreFailoverProcesses` hook. The hook only runs in the current raft leader. `orc-handler` is responsible for picking a suitable candidate for failover and running `cmd/failover` in the candidate's `mysql` container through the Kubernetes exec API (the same API `kubectl exec` uses, called directly from `orc-handler`).

`cmd/orc-handler` picks a replica as the failover target from the replicas of failed primary according to:
1. Replica should have no problems reported by Orchestrator
2. It should report the furthest `ExecBinlogCoordinates`
3. In case of a tie, lower hostname wins so `cluster1-mysql-1` is preferred over `cluster1-mysql-2`

`PreFailoverProcesses` runs before Orchestrator attempts any promotion. If the commands in the hook all exit with 0, Orchestrator will continue with promotion. If any command fails, Orchestrator will retry after `RecoveryPeriodBlockSeconds`, or once the recovery is acknowledged but the problem continues. `RecoveryPeriodBlockSeconds` is set to failover timeout + 1 hour by default. Recoveries, even if failed, are acknowledged in Orchestrator's `PostFailoverProcesses` and `PostUnsuccessfulFailoverProcesses` hooks to unblock retries and future recoveries. `cmd/orc-handler` also acknowledges the failed recovery on error to unblock Orchestrator retries.

The operator acknowledges the recoveries these miss (see [Stale recovery backstop](#stale-recovery-backstop)).

Even though we've increased the `RecoveryPeriodBlockSeconds` to a much higher value than Orchestrator default (5 seconds), we need to ensure there's a single failover process running at a given moment. For this there are two locking mechanisms:
1. **`/var/lib/mysql/failover.lock` in the failover target**: Only one process can hold the lock, so a subsequent `cmd/failover` run can't perform anything in the target.
2. **Orchestrator side claim per dead source**: A claim is the file `/etc/orchestrator/config/orc-handler/claim/<failedHost>`, holding the `{recoveryUID}` of the recovery that took it. Claims prevent Orchestrator from attempting another recovery after the first one's hook has finished but its promotion hasn't completed yet. `cmd/orc-handler` writes the claim at hook entry and touches it every 10 seconds while the hook runs. As long as the claim exists and was touched in the last 5 minutes, any other recovery of the same source is aborted immediately. A claim idle for longer belongs to a recovery orchestrator lost (crash or leadership change), and the next recovery takes it over. Taking and releasing a claim happens under a non-blocking flock on `orc-handler/failover.lock`, so two hooks can't both see the source unclaimed. The one that finds the lock held is refused as if the source were claimed. When the pre hook fails, it releases the claim itself, because orchestrator abandons that recovery and runs no post hook for it. When the pre hook succeeds, the claim stays held so the promotion can complete. `orc-handler finish -source {failedHost} -uid {recoveryUID}`, the last command of both `PostFailoverProcesses` and `PostUnsuccessfulFailoverProcesses`, releases it. `finish` drops the claim only if this recovery's UID still holds it, and on release it also clears the source's `seen` mark. If no post hook ever comes, the claim goes idle after 5 minutes.

`PreFailoverProcesses` is also triggered during graceful switchovers, because orchestrator runs a switchover as a synthesized `DeadMaster` recovery. The hook receives `-failure-type '{failureType}'` and `-command '{command}'`, and `orc-handler failover` exits 0 without doing anything when either:
1. The failure type is anything other than `DeadMaster` or `DeadMasterAndSomeReplicas`. Only those recoveries promote a replica.
2. The command is `graceful-master-takeover` or `force-master-takeover`. These are planned takeovers with a live primary, so there's nothing to salvage.

Orchestrator performs the following during a graceful switchover:
1. Current primary is put in read only mode
2. Wait for replica to catch up
3. Promote

The catch-up wait is bounded by `spec.orchestrator.failover.switchoverCatchUpTimeout` (default `5m`), which the operator maps to orchestrator's `ReasonableMaintenanceReplicationLagSeconds`. Before starting a switchover, the operator downtimes the old primary in orchestrator for twice that bound and ends the downtime once the switchover returns. The downtime protects step 1. While the old primary is read-only, orchestrator's own analysis reports it as a read-only master, and with `RecoverNonWriteableMaster: true` it would set `read_only=false` again within a second. It can do that because the takeover doesn't register its recovery until after the wait. Orchestrator skips recovering a downtimed instance, and the takeover itself ignores the downtime. The downtime also expires on its own, so an operator that dies mid-switchover can't leave an instance that orchestrator refuses to recover.

## Overseeing the recovery via Operator

The operator doesn't drive a failover. It reports the outcome, gives users a way out of a blocked failover, and cleans up state orchestrator can leave behind. All of this runs in `reconcileAsyncFailover`, only for async clusters with orchestrator enabled.

### `AsyncFailoverBlocked` condition

A failover that aborts leaves every pod read-only. To make that visible in the cluster status, the operator sets the `AsyncFailoverBlocked` condition from the primary orchestrator reports:

| Status | Reason | When |
|---|---|---|
| `True` | `NoWritablePrimary` | The primary is read-only, or orchestrator's last check of it failed (`!IsLastCheckValid`). The message points at the cluster's events and at the `percona.com/force-promote-with-possible-data-loss` annotation. |
| `False` | `PrimaryWritable` | The primary is writable and orchestrator's last check of it succeeded. |

The condition is left untouched when:
- the cluster has a single MySQL pod, since there's no replica to fail over to;
- no orchestrator pod is ready, or orchestrator can't resolve the cluster or its primary, so the operator has nothing to judge by;
- the primary is downtimed for a graceful switchover (`DowntimeReasonSwitchover`). The switchover itself makes it read-only for a while, which isn't a blocked failover.

The status is only written when the condition's status or message changes.

### `percona.com/force-promote-with-possible-data-loss` annotation

This is the way out of a blocked failover, for when the old primary's binary logs can't be salvaged and losing its undelivered transactions is accepted. The annotation's value picks the replica to promote:
- a pod name, such as `cluster1-mysql-2`. It must be an instance of this cluster in orchestrator, or the request is refused;
- `"true"`, which leaves the choice to the operator. It ranks the replicas the same way `cmd/orc-handler` does (`BestCandidate`).

The operator registers the candidate with `RegisterCandidate` and runs orchestrator's `force-master-takeover`. The hook skips planned takeovers, so nothing is salvaged. Promotions, failures and refusals are reported as `FailoverForced` warning events.

The request is refused while the cluster has a writable primary. A forced takeover neither fences nor re-points the old primary, so if it still takes writes, the promoted replica misses them and the old primary runs on its own. The request is also refused when orchestrator can't resolve the cluster or its primary, including when it answers that the cluster has no master.

A stand-down can make the old primary writable between orchestrator's last poll and the takeover, so the operator checks it again first:
1. It downtimes the old primary (`force-promote`, 5 minutes), which keeps orchestrator's own recoveries off it. If the old primary is already downtimed for another reason, such as a graceful switchover, the request waits for the next reconcile: a downtime is one row per instance, so ours would replace that one and ending ours would drop it.
2. It has orchestrator re-read it (`api/refresh`).
   - If orchestrator can reach it, the request is refused, writable or not. A read-only old primary that answers can't be taken over anyway, because orchestrator's `force-master-takeover` only finds a master it sees writable.
   - If orchestrator reports it can't read the instance and its last check was already failing, the takeover goes ahead. That old primary comes back read-only, and with its replicas gone, orchestrator never makes it writable again.
   - Any other error leaves the annotation for the next reconcile: the exec into orchestrator failing, an answer that can't be parsed, or the instance read failing after a refresh that did reach it.
3. The downtime ends once the request is handled, except after a successful takeover. By then orchestrator has downtimed the old primary itself (`lost-in-recovery`), replacing ours, and ending it would drop that one.

The annotation is removed once the request is handled: promoted, failed, refused, or the candidate is already the primary. It stays, and the next reconcile retries, in four cases:
- no orchestrator pod is ready;
- the old primary is downtimed for another reason, or downtiming it fails;
- the re-read of the old primary fails in any way other than orchestrator reporting it unreachable (above);
- the takeover fails with `ErrRecoveryNotAttempted` (or registering the candidate fails). After an aborted failover, orchestrator retries the dead primary's recovery every second, and those retries hold the recovery's unique key, so a takeover can lose the race to one of them. Retrying on the next reconcile gets it through.

The annotation is served even for a single-pod cluster, where the condition above isn't set.

### Stale recovery backstop

Orchestrator blocks a new recovery of the cluster while an earlier one is active, and the hooks can fail to end one. On every reconcile that resolves the cluster, the operator acknowledges such stale recoveries.

The operator acknowledges an active recovery only when:
1. Recovery has ended (`RecoveryEndTimestamp` is set) but is still active: This is possible if a post hook's `finish` fails to acknowledge the recovery. `cmd/orc-handler` retries the acknowledgement every 5 seconds for up to 1 minute (`ackWait`), then gives up and leaves the recovery to the operator.
2. Recovery has never ended, no orchestrator pod claims it, and it started more than 5 minutes ago: This is possible if the Orchestrator pod that was running the hook was killed mid-failover. The operator runs `orc-handler claims` in every running orchestrator pod to collect the live claims. A hook dies with its pod, so pods that aren't running are skipped. If any pod can't be read, the operator acknowledges nothing in this case, because it can't tell a claim from no claim. The start time comes from the timestamp prefix of the recovery UID, not from the audit's start timestamp, which orchestrator rewrites whenever it rebuilds its database from the raft log. The 5 minutes match the claim idle bound (`RecoveryClaimIdle`), and they give a newly registered recovery time to reach its hook and take the claim.

### `discoverMissingInstances`

Orchestrator only knows the instances it has discovered, and discovery used to be one-shot. It happened when the peer list changed (`build/orc-add_mysql_nodes.sh`, which gives up on a node it can't discover and leaves it to the operator), plus the operator's discovery of the MySQL service name, which reaches whichever single pod the name resolves to. A pod that wasn't reachable at that moment stayed out of the topology. A failover then silently skips it: the pod is never a candidate and is never re-pointed to the new primary.

On every async reconcile, `reconcileReplication` now compares orchestrator's cluster instances with the MySQL pods. For each pod orchestrator doesn't know, it runs `discover` on the pod's own hostname (`<pod>.<mysql-service>.<namespace>`). Pods whose `mysql` container hasn't passed its startup probe are skipped, since there's nothing to discover yet. A failed discovery is logged and retried on the next reconcile. It doesn't fail the reconcile.

## Configuration

### User API

The failover is configured under `spec.orchestrator.failover`:

```yaml
spec:
  orchestrator:
    failover:
      timeout: 6h
      onTimeout: Abort
      switchoverCatchUpTimeout: 5m
```

| Field | Default | Meaning |
|---|---|---|
| `timeout` | `6h` | How long the cluster tries to recover the transactions stranded on the dead primary. It spans every retry and is counted from the first attempt (see [retry semantics](#what-if-the-old-primarys-pod-is-unreachable)). |
| `onTimeout` | `Abort` | What happens once `timeout` expires: `Abort` or `ForceWithPossibleDataLoss`. |
| `switchoverCatchUpTimeout` | `5m` | How long a graceful switchover waits for the candidate to catch up before promoting it. |

The block only applies to async clusters. Setting it on a group replication cluster fails validation. The whole mechanism, meaning the hooks and every derived orchestrator setting, is only rendered for `crVersion` 1.3.0 or later. On an older `crVersion` the block is validated but has no effect.

The operator derives these orchestrator settings from the block, and it ignores any user override of them in `spec.orchestrator.configuration`:

| Orchestrator key | Derived from |
|---|---|
| `PreFailoverProcesses` | `timeout`, `onTimeout` (passed to `orc-handler failover`) |
| `RecoveryPeriodBlockSeconds` | `timeout` + 1h |
| `UnseenInstanceForgetHours` | ceil(`timeout` + 1h) |
| `ReasonableMaintenanceReplicationLagSeconds` | `switchoverCatchUpTimeout` |

The post hooks and orchestrator's own promotion gates (`FailMasterPromotionOnLagMinutes`, `DelayMasterPromotionIfSQLThreadNotUpToDate`) are also closed to overrides from 1.3.0 on. See [forced orchestrator settings](#forced-orchestrator-settings).

### `onTimeout` behaviour

Once the budget is spent, every later attempt goes straight to the `onTimeout` policy without fetching anything.

**`Abort`** (default): the hook fails, so orchestrator abandons the recovery and every pod stays read-only. The hook records a `FailoverBlocked` warning event that names the source and the timeout and points at the `percona.com/force-promote-with-possible-data-loss` annotation. Orchestrator keeps retrying, and each retry fails the same way at once. The event is deduplicated per source (see [events](#events)). The cluster stays blocked until someone annotates it.

**`ForceWithPossibleDataLoss`**: the hook picks the candidate the same way a normal failover does, but skips the salvage. Before promoting, it still runs `failover -probe` on the candidate (see [probe timing](#what-if-the-old-primary-comes-back-and-becomes-ready-to-serve-while-replica-applies-relay-logs)). If the old primary is back as a primary (see step 2 of the probe) and holds every transaction the candidate has, the candidate stands down instead, and nothing is promoted: promoting would leave two writable primaries. Otherwise the hook registers the candidate (`RegisterCandidate`), records a `FailoverForced` warning event saying the old primary's undelivered transactions are lost, and exits 0 so orchestrator promotes it. If the source has no replicas, the hook exits 0 with nothing to promote.

### Clock restart on a raft leader change

The hook's state (the `seen` marks, the claims and the event dedup marks under `orc-handler/`) lives on `/etc/orchestrator/config`, which is an emptyDir on each orchestrator pod. The hook only runs on the raft leader, so after a leader change mid-failover the new leader has no `seen` mark for the source, and the next attempt starts the budget from scratch. The same happens when the leader pod is deleted and recreated.

This only ever delays the `onTimeout` decision: the worst case is up to one extra `timeout` per leader change. This is also why the operator's backstop reads claims from every running orchestrator pod rather than one. It's also why a leader change can repeat an event that was already deduplicated on the old leader.

### Forced orchestrator settings

The pre hook is the only thing that decides whether the candidate is safe to promote. So from `crVersion` 1.3.0 on, the operator pins these orchestrator settings and ignores any user override of them in `spec.orchestrator.configuration`:

| Key | Value | Why |
|---|---|---|
| `FailMasterPromotionOnLagMinutes` | `0` | `ReplicationLagQuery` reads `sys_operator.heartbeat`, whose newest row came from the dead primary. The "lag" it measures is really the wall-clock time since the failure, and it grows with the hook's own runtime. Any non-zero value vetoes failovers. The orchestrator image sets it to `10`, so a hook running longer than 10 minutes would always be vetoed without this. |
| `DelayMasterPromotionIfSQLThreadNotUpToDate` | `false` | Keeps orchestrator from adding its own apply gate on top of the hook, which has already waited for the applier to work through the spliced events. `FailMasterPromotionIfSQLThreadNotUpToDate` stays `false` for the same reason. It was already reserved before 1.3.0. |
| `UnseenInstanceForgetHours` | ceil(`timeout` + 1h) | Orchestrator forgets an instance it hasn't seen for this long, and the dead primary is exactly such an instance. Once its row is gone, there's no `DeadMaster` analysis left to retry the hook for, and no primary for a forced takeover to demote. So it has to outlast the failover timeout. |
| `RecoveryPeriodBlockSeconds` | `timeout` + 1h | Orchestrator measures this from the start of a recovery. A shorter value would let it register a second recovery of the same failure while the hook is still working on the first. `finish` acknowledges the recovery when it's over, which ends the block early, so a promoted cluster isn't left unrecoverable for this long. |
| `ReasonableMaintenanceReplicationLagSeconds` | `switchoverCatchUpTimeout` | Bounds the graceful switchover's catch-up wait. |
| `PreFailoverProcesses`, `PostFailoverProcesses`, `PostUnsuccessfulFailoverProcesses` | the `orc-handler` hooks | The mechanism itself. |

With the default `6h` timeout, the shipped `build/orchestrator.conf.json` already carries the derived values (`RecoveryPeriodBlockSeconds: 25200`, `UnseenInstanceForgetHours: 7`).

### Hook deadline (`hookTimeout`)

A single `orc-handler failover` invocation runs under a deadline of:

```
hookTimeout = timeout + sourcePodWait (2m) + probeTimeout (1m) + ackWait (1m) + execSlack (10s)
```

Each term is a wait that one attempt may go through on top of the worker's own run:

| Term | Covers |
|---|---|
| `timeout` | The worker itself, which may spend whatever is left of the budget |
| `sourcePodWait` (2m) | Waiting for the source pod to exist before the worker starts |
| `probeTimeout` (1m) | The `failover -probe` run after the worker, before `RegisterCandidate` |
| `ackWait` (1m) | Acknowledging the recovery when the hook fails |
| `execSlack` (10s) | Margin for the exec calls and API round trips |

This deadline is only a backstop against a hook that hangs. It doesn't enforce the budget: the worker gets the remaining budget as `failover -timeout` and stops itself. So a hook that reaches the deadline has hung somewhere, and hasn't simply run out of budget.

## Events

Every failover event is recorded on the `PerconaServerMySQL` object:

| Reason | Type | Emitted by | When |
|---|---|---|---|
| `FailoverWaiting` | Normal | `orc-handler failover` | On the first attempt for a source, the one that starts the budget. It says the cluster has no writable primary until the salvage is done, for up to `timeout`, and names the `onTimeout` policy that applies after that. |
| `FailoverBlocked` | Warning | `orc-handler failover` | The budget is spent and `onTimeout` is `Abort`. It points at the force-promote annotation. |
| `FailoverForced` | Warning | `orc-handler failover` | The budget is spent and `onTimeout` is `ForceWithPossibleDataLoss`, and a candidate is being promoted anyway. |
| `FailoverForced` | Warning | operator | The force-promote annotation was handled: promoted, failed, or refused. |
| `FailoverFailed` | Warning | `orc-handler report-failover` (`PostUnsuccessfulFailoverProcesses`) | Orchestrator gave up on a `DeadMaster`/`DeadMasterAndSomeReplicas` recovery. |

Orchestrator retries a blocked recovery every few seconds, and each retry runs the hook again. So `FailoverWaiting`, `FailoverBlocked` and `FailoverFailed` are deduplicated per source and per reason: once one is recorded, the same reason for the same source stays quiet for 5 minutes (`notifyInterval`). The reasons are tracked separately, so a failover that starts waiting and times out within 5 minutes reports both. A stand-down marks `FailoverFailed` as already sent, since the cluster has its primary back. `FailoverForced` isn't deduplicated: each one follows a single decision to promote.

The dedup marks live under `orc-handler/notified/<reason>/<source>` on the leader's emptyDir, so a raft leader change can repeat an event (see [clock restart](#clock-restart-on-a-raft-leader-change)).

## Supporting changes

These changes sit outside the failover path, but without them a failover fails or silently loses data.

### Headless service publishes not-ready addresses

**Change:** the MySQL headless service now sets `PublishNotReadyAddresses: true` for async clusters too (it already did for group replication).

**Failure it prevents:** a replica whose source died fails its readiness probe about 15 seconds later, well before orchestrator promotes it. With not-ready addresses dropped, the candidate's per-pod DNS name stopped resolving, orchestrator couldn't reach it, the promotion never landed, and the cluster stayed read-only. A NotReady candidate mid-failover is now expected and harmless.

### `server_id` per data directory

**Change:** `build/ps-entrypoint.sh` gives a fresh data directory a random `server_id` above 2^31, kept in `/var/lib/mysql/server-id`. Data directories created before this keep their ordinal-derived id.

**Failure it prevents:** the id used to come from the pod's ordinal alone, so a pod rebuilt on a new volume kept the id of the data directory it replaced. Replication filters out events carrying the replica's own `server_id`, so the rebuilt pod silently dropped every transaction its previous incarnation had written as primary. In the e2e test, a rebuilt `mysql-2` came back without the 165 transactions it had written, its pt-heartbeat row included. Once promoted it inserted that row again, and `mysql-0`, which still held it, stopped replicating on a duplicate key.

### GTID-based bootstrap election, and no blind clone

**Change:** when a pod starts, `cmd/bootstrap/async` now:
- elects the primary it replicates from by GTID: the peer whose executed set holds every other peer's. A tie is settled among those complete peers only, preferring a peer other than the one asking. If no peer holds everything, the peers have diverged and bootstrap fails.
- clones only when it has to. It refuses to clone if the local data directory holds transactions the donor lacks (`errAheadOfDonor`), because `CLONE INSTANCE` would drop them. It clones only when the donor has purged binary logs the local data directory still needs.

**Failure it prevents:** before, the primary was a positional guess (the first peer other than itself), and a pod cloned on effectively every restart. A failed primary that restarted before the hook fetched its binary logs would be cloned from a replica, wiping out exactly the transactions the salvage exists to recover. With the positional guess, and later with an unrestricted tie-break, a primary killed mid-failover could restart level with the replica the hook had caught up and pick a peer that was behind. It then came back replicating through that peer, and the chain stayed after the promotion.

### More discovery retries

**Change:** `build/orc-add_mysql_nodes.sh` retries discovering a peer 30 times, 2 seconds apart (it used to be 5 times, 1 second apart), then leaves the node to the operator's [`discoverMissingInstances`](#discovermissinginstances).

**Failure it prevents:** with not-ready addresses published, a pod shows up in the peer list before its `mysqld` is up. Five quick attempts gave up on it, and since discovery was one-shot, it stayed out of orchestrator's topology. A failover then skipped it.

## Assumptions and limitations

- **Durable commits on the primary.** "Zero data loss" assumes `sync_binlog=1` and `innodb_flush_log_at_trx_commit=1` on every pod. These are MySQL's defaults and the operator doesn't change them, but it doesn't enforce them either. With either relaxed, a primary that crashes can lose transactions it acknowledged to clients before they reached the binary log on disk, and no salvage can bring those back.
- **Orchestrator behaviour the stand-down relies on.** The design assumes orchestrator runs `NoWriteableMaster` recovery only for a reachable read-only master with at least one replica whose IO and SQL threads both run, skips downtimed instances in periodic recoveries, and finds the master for `force-master-takeover` only through a writable backend row.
- **Writable but unreachable primary.** A primary that is writable but unreachable from orchestrator (a network partition, a paused process) can still take writes after a promotion. Neither the hook nor the force-promote check can see it.
- **Read-only old primary that holds less.** A reachable, read-only old primary that holds less than the candidate (only possible with relaxed durability settings) can't be force-promoted past.
- **Durable relay logs on the candidate.** The claim that a transaction in a replica's relay log is durable depends on the replica's relay log settings. The operator sets none of them, so MySQL's defaults apply. `sync_relay_log=10000` means a host crash can lose recently received relay log events. With `relay_log_recovery=ON`, a restart discards every relay log event not yet applied, the spliced events included, and resets the received position to the applied one. Either way the next failover attempt reads the replica's position again and fetches the missing range from the source's sidecar. This costs a retry rather than data, as long as the old primary's binary logs can still be served.
- **Purged binary logs.** If the source no longer has the binary log the replica last read, because it expired or was purged, the sidecar answers `404`. Every attempt fails the same way until the timeout expires and `onTimeout` applies. Nothing can salvage those transactions.
- **Lost PVC on the old primary.** The salvage reads the old primary's binary logs from its data volume. If the volume is gone, the transactions only it held are gone too. Nothing can salvage them, and the failover can only end through `onTimeout` or the force-promote annotation.
