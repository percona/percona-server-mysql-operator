package ps

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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

	ready, msg, err := r.isAsyncReady(t.Context(), cr)
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
