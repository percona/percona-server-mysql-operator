package ps

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

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
		ObjectMeta: metav1.ObjectMeta{
			Name:      orchestrator.PodName(cr, 0),
			Namespace: cr.Namespace,
			Labels:    orchestrator.MatchLabels(cr),
		},
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      orchestrator.PodName(cr, 0),
			Namespace: cr.Namespace,
			Labels:    orchestrator.MatchLabels(cr),
		},
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
