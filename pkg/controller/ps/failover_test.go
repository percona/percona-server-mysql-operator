package ps

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/orchestrator"
)

func TestHasWritablePrimary(t *testing.T) {
	tests := map[string]struct {
		primary  orchestrator.Instance
		writable bool
	}{
		"live and writable": {
			primary:  orchestrator.Instance{Alias: "cluster1-mysql-0", IsLastCheckValid: true},
			writable: true,
		},
		"read only": {
			primary: orchestrator.Instance{Alias: "cluster1-mysql-0", IsLastCheckValid: true, ReadOnly: true},
		},
		"dead but remembered as writable": {
			primary: orchestrator.Instance{Alias: "cluster1-mysql-0"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.writable, hasWritablePrimary(&tt.primary))
		})
	}
}

// A forced takeover leaves the old primary writing and never re-points it, so
// with a writable primary it splits the cluster instead of unblocking it.
func TestReconcileForcePromoteRefusesWritablePrimary(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "force-promote")
	require.NoError(t, err)
	cr.Annotations = map[string]string{naming.AnnotationForcePromote.String(): "async-failover-mysql-1"}

	recorder := record.NewFakeRecorder(10)
	r := &PerconaServerMySQLReconciler{
		Client:   fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr).Build(),
		Recorder: recorder,
	}

	primary := &orchestrator.Instance{Alias: "async-failover-mysql-0", IsLastCheckValid: true}

	// A nil orcPod and ClientCmd make any call to orchestrator panic.
	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, nil, cr.ClusterHint(), primary))

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	event := <-recorder.Events
	assert.Contains(t, event, naming.EventFailoverForced)
	assert.Contains(t, event, "async-failover-mysql-0 is a writable primary")
}

const (
	forcePromoteCluster    = "async-failover-mysql-0:3306"
	forcePromoteOldPrimary = "async-failover-mysql-0"
)

func forcePromoteScripts(t *testing.T, refresh []fakeClientScript, takeover ...fakeClientScript) []fakeClientScript {
	t.Helper()

	instances, err := json.Marshal([]orchestrator.Instance{
		{Alias: forcePromoteOldPrimary, ClusterName: forcePromoteCluster},
		{Alias: "async-failover-mysql-1", ClusterName: forcePromoteCluster, IsLastCheckValid: true},
	})
	require.NoError(t, err)

	scripts := []fakeClientScript{
		{cmd: orcURL("api/cluster/" + forcePromoteCluster), stdout: instances},
		{cmd: orcURL("api/begin-downtime/" + forcePromoteOldPrimary + "/3306/percona-server-mysql-operator/force-promote/300s"), stdout: downtimeResp},
	}
	scripts = append(scripts, refresh...)
	scripts = append(scripts, takeover...)

	return append(scripts, fakeClientScript{cmd: orcURL("api/end-downtime/" + forcePromoteOldPrimary + "/3306"), stdout: okResp(t)})
}

func okResp(t *testing.T) []byte {
	t.Helper()

	ok, err := json.Marshal(map[string]string{"Code": "OK"})
	require.NoError(t, err)

	return ok
}

// reachable is what orchestrator answers when it can read the old primary now.
func reachable(t *testing.T, readOnly bool) []fakeClientScript {
	t.Helper()

	instance, err := json.Marshal(orchestrator.Instance{
		Key:              orchestrator.InstanceKey{Hostname: forcePromoteOldPrimary, Port: 3306},
		Alias:            forcePromoteOldPrimary,
		ReadOnly:         readOnly,
		IsLastCheckValid: true,
	})
	require.NoError(t, err)

	return []fakeClientScript{
		{cmd: orcURL("api/refresh/" + forcePromoteOldPrimary + "/3306"), stdout: okResp(t)},
		{cmd: orcURL("api/instance/" + forcePromoteOldPrimary + "/3306"), stdout: instance},
	}
}

func unreachable(t *testing.T) []fakeClientScript {
	t.Helper()

	failed, err := json.Marshal(map[string]string{"Code": "ERROR", "Message": "dial tcp: i/o timeout"})
	require.NoError(t, err)

	return []fakeClientScript{{cmd: orcURL("api/refresh/" + forcePromoteOldPrimary + "/3306"), stdout: failed}}
}

func takeover(t *testing.T, result []byte) []fakeClientScript {
	t.Helper()

	return []fakeClientScript{
		{cmd: orcURL("api/register-candidate/async-failover-mysql-1/3306/prefer"), stdout: okResp(t)},
		{cmd: orcURL("api/force-master-takeover/" + forcePromoteCluster + "/async-failover-mysql-1/3306"), stdout: result},
	}
}

func forcePromoteReconciler(t *testing.T, fc *fakeClient) (*PerconaServerMySQLReconciler, *apiv1.PerconaServerMySQL, *record.FakeRecorder) {
	t.Helper()

	cr, err := readDefaultCR("async-failover", "force-promote")
	require.NoError(t, err)
	cr.Annotations = map[string]string{naming.AnnotationForcePromote.String(): "async-failover-mysql-1"}

	recorder := record.NewFakeRecorder(10)

	return &PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr).Build(),
		ClientCmd: fc,
		Recorder:  recorder,
	}, cr, recorder
}

func deadPrimary() *orchestrator.Instance {
	return &orchestrator.Instance{Key: orchestrator.InstanceKey{Hostname: forcePromoteOldPrimary, Port: 3306}, Alias: forcePromoteOldPrimary}
}

func TestReconcileForcePromoteRetriesTakeoverNotAttempted(t *testing.T) {
	notAttempted, err := json.Marshal(map[string]string{
		"Code":    "ERROR",
		"Message": "Unexpected error: recovery not attempted. This should not happen",
	})
	require.NoError(t, err)

	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t), takeover(t, notAttempted)...)}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	err = r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary())
	require.ErrorIs(t, err, orchestrator.ErrRecoveryNotAttempted)
	assert.Equal(t, len(fc.scripts), fc.execCount, "the downtime must be ended on a retry too")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Equal(t, "async-failover-mysql-1", cr.Annotations[naming.AnnotationForcePromote.String()])
	assert.Empty(t, recorder.Events)
}

func TestReconcileForcePromoteRefusesPrimaryWritableOnRefresh(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, reachable(t, false))}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	primary := deadPrimary()
	primary.ReadOnly = true
	primary.IsLastCheckValid = true

	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, primary))
	assert.Equal(t, len(fc.scripts), fc.execCount, "no takeover may follow a writable refresh")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	assert.Contains(t, <-recorder.Events, forcePromoteOldPrimary+" is a writable primary")
}

func TestReconcileForcePromoteRefusesReachableReadOnlyPrimary(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, reachable(t, true))}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	assert.Equal(t, len(fc.scripts), fc.execCount, "no takeover may follow a reachable primary")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	event := <-recorder.Events
	assert.Contains(t, event, forcePromoteOldPrimary+" is back read-only")
	assert.Contains(t, event, "IO and SQL threads running")
}

func TestReconcileForcePromoteTakesOverUnreachablePrimary(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t), takeover(t, okResp(t))...)}
	r, cr, _ := forcePromoteReconciler(t, fc)

	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	assert.Equal(t, len(fc.scripts)-1, fc.execCount,
		"orchestrator's lost-in-recovery downtime has replaced ours, and ending it would drop that one")
}

func TestReconcileForcePromoteEndsDowntimeOnFailedTakeover(t *testing.T) {
	failed, err := json.Marshal(map[string]string{"Code": "ERROR", "Message": "no candidate"})
	require.NoError(t, err)

	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t), takeover(t, failed)...)}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	assert.Equal(t, len(fc.scripts), fc.execCount)

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	assert.Contains(t, <-recorder.Events, "Could not force the promotion of async-failover-mysql-1")
}

func TestReconcileForcePromoteRetriesForeignDowntime(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t), takeover(t, okResp(t))...)}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	primary := deadPrimary()
	primary.IsDowntimed = true
	primary.DowntimeOwner = orchestrator.DowntimeOwner
	primary.DowntimeReason = orchestrator.DowntimeReasonSwitchover

	err := r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, primary)
	require.Error(t, err)
	assert.Contains(t, err.Error(), orchestrator.DowntimeReasonSwitchover)
	assert.Equal(t, 1, fc.execCount, "only the cluster read may run, never a downtime")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Contains(t, cr.Annotations, naming.AnnotationForcePromote.String())
	assert.Empty(t, recorder.Events)
}

func TestReconcileForcePromoteRetriesFailedDowntime(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t), takeover(t, okResp(t))...)}
	fc.scripts[1].err = errors.New("connection reset")
	r, cr, recorder := forcePromoteReconciler(t, fc)

	require.Error(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	// Every exec runs in script order, so a refresh, takeover or end-downtime
	// would have consumed the next script.
	assert.Equal(t, 2, fc.execCount)

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Contains(t, cr.Annotations, naming.AnnotationForcePromote.String())
	assert.Empty(t, recorder.Events)
}

func TestReconcileForcePromoteRefusesUnknownPrimary(t *testing.T) {
	r, cr, recorder := forcePromoteReconciler(t, nil)
	r.ClientCmd = nil

	require.NoError(t, r.reconcileForcePromote(t.Context(), cr, nil, forcePromoteCluster, &orchestrator.Instance{}))

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	assert.Contains(t, <-recorder.Events, "orchestrator does not know the cluster's primary")
}

func TestReconcileForcePromoteRetriesFailedReadAfterRefresh(t *testing.T) {
	failed, err := json.Marshal(map[string]string{"Code": "ERROR", "Message": "instance not found"})
	require.NoError(t, err)

	refresh := []fakeClientScript{
		{cmd: orcURL("api/refresh/" + forcePromoteOldPrimary + "/3306"), stdout: okResp(t)},
		{cmd: orcURL("api/instance/" + forcePromoteOldPrimary + "/3306"), stdout: failed},
	}
	fc := &fakeClient{scripts: forcePromoteScripts(t, refresh)}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	require.Error(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	assert.Equal(t, len(fc.scripts), fc.execCount, "no takeover may follow, and the downtime must end")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Contains(t, cr.Annotations, naming.AnnotationForcePromote.String())
	assert.Empty(t, recorder.Events)
}

func TestReconcileForcePromoteRetriesRefreshTransportFailure(t *testing.T) {
	refresh := []fakeClientScript{
		{cmd: orcURL("api/refresh/" + forcePromoteOldPrimary + "/3306"), err: errors.New("connection reset")},
	}
	fc := &fakeClient{scripts: forcePromoteScripts(t, refresh)}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	require.Error(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, deadPrimary()))
	assert.Equal(t, len(fc.scripts), fc.execCount, "no takeover may follow, and the downtime must end")

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Contains(t, cr.Annotations, naming.AnnotationForcePromote.String())
	assert.Empty(t, recorder.Events)
}
func TestReconcileForcePromoteRetriesFailedRefreshOfReachablePrimary(t *testing.T) {
	fc := &fakeClient{scripts: forcePromoteScripts(t, unreachable(t))}
	r, cr, recorder := forcePromoteReconciler(t, fc)

	primary := deadPrimary()
	primary.ReadOnly = true
	primary.IsLastCheckValid = true

	require.Error(t, r.reconcileForcePromote(t.Context(), cr, &corev1.Pod{}, forcePromoteCluster, primary))
	assert.Equal(t, len(fc.scripts), fc.execCount)

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.Contains(t, cr.Annotations, naming.AnnotationForcePromote.String())
	assert.Empty(t, recorder.Events)
}

// An orphaned primary claims the cluster's alias as well, so a promotion
// requested while orchestrator sees two live clusters could land on either.
func TestReconcileAsyncFailoverRefusesSplitTopology(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "split")
	require.NoError(t, err)
	cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeAsync
	cr.Spec.MySQL.Size = 3
	cr.Spec.Orchestrator.Enabled = true
	cr.Spec.Orchestrator.Size = 3
	cr.Annotations = map[string]string{naming.AnnotationForcePromote.String(): "true"}

	orcPod := &corev1.Pod{
		Name:      orchestrator.PodName(cr, 0),
		Namespace: cr.Namespace,
		Labels:    orchestrator.MatchLabels(cr),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	instances, err := json.Marshal([]orchestrator.Instance{
		{Alias: "async-failover-mysql-0", ClusterName: "async-failover-mysql-0:3306"},
		{Alias: "async-failover-mysql-1", ClusterName: "async-failover-mysql-0:3306", IsLastCheckValid: true},
		{Alias: "async-failover-mysql-2", ClusterName: "async-failover-mysql-2:3306", IsLastCheckValid: true},
	})
	require.NoError(t, err)

	fc := &fakeClient{scripts: []fakeClientScript{allInstancesScriptWith(instances)}}
	recorder := record.NewFakeRecorder(10)
	r := &PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr, orcPod).Build(),
		ClientCmd: fc,
		Recorder:  recorder,
	}

	require.NoError(t, r.reconcileAsyncFailover(t.Context(), cr))

	// Nothing past the lookup: no primary read, no candidate, no takeover.
	assert.Equal(t, 1, fc.execCount)

	require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))
	assert.NotContains(t, cr.Annotations, naming.AnnotationForcePromote.String())

	require.Len(t, recorder.Events, 1)
	event := <-recorder.Events
	assert.Contains(t, event, naming.EventFailoverForced)
	assert.Contains(t, event, "orchestrator sees more than one live cluster: async-failover-mysql-0:3306, async-failover-mysql-2:3306")
}

// Readiness has to see the orphan too: judged by whichever cluster holds the
// alias, a cluster whose old primary is running on its own looks healthy.
func TestIsAsyncReadyReportsSplitTopology(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "split")
	require.NoError(t, err)

	orcPod := &corev1.Pod{
		Name:      orchestrator.PodName(cr, 0),
		Namespace: cr.Namespace,
		Labels:    orchestrator.MatchLabels(cr),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	instances, err := json.Marshal([]orchestrator.Instance{
		{Alias: "async-failover-mysql-0", ClusterName: "async-failover-mysql-0:3306", IsLastCheckValid: true},
		{Alias: "async-failover-mysql-1", ClusterName: "async-failover-mysql-0:3306", IsLastCheckValid: true},
		{Alias: "async-failover-mysql-2", ClusterName: "async-failover-mysql-2:3306", IsLastCheckValid: true},
	})
	require.NoError(t, err)

	r := &PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(orcPod).Build(),
		ClientCmd: &fakeClient{scripts: []fakeClientScript{allInstancesScriptWith(instances)}},
	}

	ready, msg, _, err := r.isAsyncReady(t.Context(), cr)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.Contains(t, msg, "orchestrator sees more than one live cluster: async-failover-mysql-0:3306, async-failover-mysql-2:3306")
}

// A failover renames the cluster after the promoted primary while the recovery
// that promoted it keeps the old name, so the sweep has to find it by alias.
func TestReconcileAsyncFailoverAcksStaleRecoveryOfRenamedCluster(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "renamed")
	require.NoError(t, err)
	cr.Spec.MySQL.ClusterType = apiv1.ClusterTypeAsync
	cr.Spec.MySQL.Size = 3
	cr.Spec.Orchestrator.Enabled = true
	cr.Spec.Orchestrator.Size = 3

	orcPod := &corev1.Pod{
		Name:      orchestrator.PodName(cr, 0),
		Namespace: cr.Namespace,
		Labels:    orchestrator.MatchLabels(cr),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	instances, err := json.Marshal([]orchestrator.Instance{
		{Alias: "async-failover-mysql-0", ClusterName: "async-failover-mysql-2:3306", IsLastCheckValid: true},
		{Alias: "async-failover-mysql-1", ClusterName: "async-failover-mysql-2:3306", IsLastCheckValid: true},
		{Alias: "async-failover-mysql-2", ClusterName: "async-failover-mysql-2:3306", IsLastCheckValid: true},
	})
	require.NoError(t, err)

	uid := "1790238958385238030:553a064110b0d0e2356aac15ad73eabfbeb9aff81e0be4b264d20e27470bc7ee"
	recs, err := json.Marshal([]orchestrator.Recovery{
		{UID: uid, IsActive: true, IsSuccessful: true, RecoveryEndTimestamp: "2026-09-24 08:38:17"},
	})
	require.NoError(t, err)
	acked, err := json.Marshal(map[string]string{"Code": "OK"})
	require.NoError(t, err)

	fc := &fakeClient{scripts: []fakeClientScript{
		allInstancesScriptWith(instances),
		{
			cmd:    orcURL(fmt.Sprintf("api/audit-recovery/alias/%s?unacknowledged=true", cr.ClusterHint())),
			stdout: recs,
		},
		{
			cmd:    orcURL(fmt.Sprintf("api/ack-recovery/uid/%s?comment=%s", uid, url.QueryEscape(staleRecoveryComment))),
			stdout: acked,
		},
		{
			cmd: orcURL("api/master/async-failover-mysql-2:3306"),
			err: errors.New("stop here"),
		},
	}}
	r := &PerconaServerMySQLReconciler{
		Client:    fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cr, orcPod).Build(),
		ClientCmd: fc,
		Recorder:  record.NewFakeRecorder(10),
	}

	require.NoError(t, r.reconcileAsyncFailover(t.Context(), cr))
	assert.Equal(t, len(fc.scripts), fc.execCount)
}

func TestStaleRecoveries(t *testing.T) {
	now := time.Unix(0, 1790100000000000000)
	uidAt := func(ago time.Duration, hash string) string {
		return fmt.Sprintf("%d:%s", now.Add(-ago).UnixNano(), hash)
	}

	finished := orchestrator.Recovery{UID: uidAt(time.Hour, "finished"), IsActive: true, IsSuccessful: true, RecoveryEndTimestamp: "2026-09-22 18:06:27"}
	abandoned := orchestrator.Recovery{UID: uidAt(time.Hour, "abandoned"), IsActive: true}
	fresh := orchestrator.Recovery{UID: uidAt(time.Minute, "fresh"), IsActive: true}
	expired := orchestrator.Recovery{UID: uidAt(8*time.Hour, "expired"), RecoveryEndTimestamp: "2026-09-22 10:06:27"}
	noTime := orchestrator.Recovery{UID: "garbage", IsActive: true}

	tests := map[string]struct {
		recs        []orchestrator.Recovery
		live        []string
		claimsKnown bool
		want        []orchestrator.Recovery
	}{
		"a finished recovery whose acknowledgement was lost": {
			recs: []orchestrator.Recovery{finished},
			want: []orchestrator.Recovery{finished},
		},
		"an abandoned recovery nobody holds a claim for": {
			recs:        []orchestrator.Recovery{abandoned},
			claimsKnown: true,
			want:        []orchestrator.Recovery{abandoned},
		},
		"a recovery whose hook is still in flight": {
			recs:        []orchestrator.Recovery{abandoned},
			live:        []string{abandoned.UID},
			claimsKnown: true,
		},
		"a recovery too young to have claimed its source": {
			recs:        []orchestrator.Recovery{fresh},
			claimsKnown: true,
		},
		"an unfinished recovery while the claims are unknown": {
			recs: []orchestrator.Recovery{abandoned, finished},
			want: []orchestrator.Recovery{finished},
		},
		"a recovery out of its active period blocks nothing": {
			recs:        []orchestrator.Recovery{expired},
			claimsKnown: true,
		},
		"a uid that says nothing about its age": {
			recs:        []orchestrator.Recovery{noTime},
			claimsKnown: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			live := make(map[string]bool)
			for _, uid := range tt.live {
				live[uid] = true
			}

			assert.Equal(t, tt.want, staleRecoveries(tt.recs, live, tt.claimsKnown, now))
		})
	}
}

// A lagging replica is still replicating, so it is reported in its own
// condition instead of holding the whole cluster back from ready.
func TestIsAsyncReadyIgnoresReplicationLag(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "lag")
	require.NoError(t, err)

	orcPod := &corev1.Pod{
		Name:      orchestrator.PodName(cr, 0),
		Namespace: cr.Namespace,
		Labels:    orchestrator.MatchLabels(cr),
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.ContainersReady, Status: corev1.ConditionTrue}},
		},
	}

	const cluster = "async-failover-mysql-0:3306"
	lag := func(seconds int64) sql.NullInt64 { return sql.NullInt64{Int64: seconds, Valid: true} }
	primary := orchestrator.InstanceKey{Hostname: "async-failover-mysql-0", Port: 3306}

	tests := map[string]struct {
		instances []orchestrator.Instance
		ready     bool
		msg       string
		lagging   []string
	}{
		"no lag": {
			instances: []orchestrator.Instance{
				{Alias: "async-failover-mysql-0", ClusterName: cluster, IsLastCheckValid: true},
				{Alias: "async-failover-mysql-1", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true},
			},
			ready:   true,
			lagging: []string{},
		},
		"lag only": {
			instances: []orchestrator.Instance{
				{Alias: "async-failover-mysql-0", ClusterName: cluster, IsLastCheckValid: true},
				{Alias: "async-failover-mysql-2", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true, Problems: []string{"replication_lag"}, ReplicationLagSeconds: lag(75)},
				{Alias: "async-failover-mysql-1", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true, Problems: []string{"replication_lag"}, ReplicationLagSeconds: lag(142)},
			},
			ready:   true,
			lagging: []string{"async-failover-mysql-1", "async-failover-mysql-2"},
		},
		"lag with another problem": {
			instances: []orchestrator.Instance{
				{Alias: "async-failover-mysql-0", ClusterName: cluster, IsLastCheckValid: true},
				{Alias: "async-failover-mysql-1", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true, Problems: []string{"not_replicating", "replication_lag"}},
			},
			msg:     "async-failover-mysql-1: [not_replicating]",
			lagging: []string{"async-failover-mysql-1"},
		},
		"lagging primary": {
			instances: []orchestrator.Instance{
				{Alias: "async-failover-mysql-0", ClusterName: cluster, IsLastCheckValid: true, Problems: []string{"replication_lag"}, ReplicationLagSeconds: lag(320)},
				{Alias: "async-failover-mysql-1", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true},
			},
			ready:   true,
			lagging: []string{},
		},
		"downtimed lagging replica": {
			instances: []orchestrator.Instance{
				{Alias: "async-failover-mysql-0", ClusterName: cluster, IsLastCheckValid: true},
				{Alias: "async-failover-mysql-1", ClusterName: cluster, MasterKey: primary, IsLastCheckValid: true, IsDowntimed: true, Problems: []string{"replication_lag"}},
			},
			ready:   true,
			lagging: []string{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			instances, err := json.Marshal(tt.instances)
			require.NoError(t, err)

			r := &PerconaServerMySQLReconciler{
				Client: fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(orcPod).Build(),
				ClientCmd: &fakeClient{scripts: []fakeClientScript{
					allInstancesScriptWith(instances),
					{cmd: orcURL("api/cluster/" + cluster), stdout: instances},
				}},
			}

			ready, msg, lagging, err := r.isAsyncReady(t.Context(), cr)
			require.NoError(t, err)
			assert.Equal(t, tt.ready, ready)
			assert.Equal(t, tt.msg, msg)

			aliases := []string{}
			for _, i := range lagging {
				aliases = append(aliases, i.Alias)
			}
			assert.Equal(t, tt.lagging, aliases)
		})
	}
}

func TestReplicationLagCondition(t *testing.T) {
	cr, err := readDefaultCR("async-failover", "lag")
	require.NoError(t, err)
	cr.Generation = 3

	cond := replicationLagCondition(cr, []*orchestrator.Instance{})
	assert.Equal(t, apiv1.ConditionReplicationLagging, cond.Type)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, reasonNoReplicationLag, cond.Reason)
	assert.EqualValues(t, 3, cond.ObservedGeneration)

	cond = replicationLagCondition(cr, []*orchestrator.Instance{
		{Alias: "async-failover-mysql-1", ReplicationLagSeconds: sql.NullInt64{Int64: 142, Valid: true}},
		{Alias: "async-failover-mysql-2"},
	})
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, reasonReplicationLagDetected, cond.Reason)
	assert.Contains(t, cond.Message, "async-failover-mysql-1 (142s), async-failover-mysql-2")
	assert.Contains(t, cond.Message, "ReasonableReplicationLagSeconds")
}
