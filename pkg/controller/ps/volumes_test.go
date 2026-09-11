package ps

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestPVCOrdinal(t *testing.T) {
	const stsName = "cluster1-mysql"

	tests := []struct {
		name    string
		pvcName string
		ordinal int
		ok      bool
	}{
		{name: "first replica", pvcName: "datadir-cluster1-mysql-0", ordinal: 0, ok: true},
		{name: "tenth replica", pvcName: "datadir-cluster1-mysql-9", ordinal: 9, ok: true},
		{name: "two digit ordinal", pvcName: "datadir-cluster1-mysql-10", ordinal: 10, ok: true},

		{name: "another cluster", pvcName: "datadir-cluster2-mysql-0", ok: false},
		{name: "another volume", pvcName: "backup-cluster1-mysql-0", ok: false},
		{name: "no ordinal", pvcName: "datadir-cluster1-mysql-", ok: false},
		{name: "not a number", pvcName: "datadir-cluster1-mysql-primary", ok: false},

		// a statefulset never creates these, and accepting them would let a claim
		// no replica can ever use take part in a resize
		{name: "negative ordinal", pvcName: "datadir-cluster1-mysql--1", ok: false},
		{name: "explicitly signed ordinal", pvcName: "datadir-cluster1-mysql-+1", ok: false},
		{name: "zero padded ordinal", pvcName: "datadir-cluster1-mysql-01", ok: false},
		{name: "negative zero", pvcName: "datadir-cluster1-mysql--0", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ordinal, ok := pvcOrdinal(tt.pvcName, stsName)
			if ok != tt.ok {
				t.Fatalf("pvcOrdinal(%q) ok = %v, want %v (ordinal %d)", tt.pvcName, ok, tt.ok, ordinal)
			}
			if ok && ordinal != tt.ordinal {
				t.Fatalf("pvcOrdinal(%q) = %d, want %d", tt.pvcName, ordinal, tt.ordinal)
			}
		})
	}
}

// A selected PVC can exist before it reports a capacity, for example a claim
// that is still Pending during a scale up. Sizing it must not panic, and it must
// not look like a finished resize, so that the operator waits for the claim.
func TestPVCSizeWithoutCapacity(t *testing.T) {
	requested := resource.MustParse("3Gi")

	tests := []struct {
		name string
		pvc  corev1.PersistentVolumeClaim
	}{
		{
			name: "pending claim",
			pvc:  corev1.PersistentVolumeClaim{},
		},
		{
			// maps that exist but carry no storage entry, which is the case the
			// review comment describes, as opposed to the nil maps above
			name: "capacity present without a storage entry",
			pvc: corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
				},
			},
		},
		{
			name: "allocatedResources present without a storage entry",
			pvc: corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					AllocatedResources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					AllocatedResourceStatuses: map[corev1.ResourceName]corev1.ClaimResourceStatus{
						corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
					},
				},
			},
		},
		{
			// the branch that reads allocatedResources, before that map is filled
			name: "resize reported before allocatedResources is set",
			pvc: corev1.PersistentVolumeClaim{
				Status: corev1.PersistentVolumeClaimStatus{
					AllocatedResourceStatuses: map[corev1.ResourceName]corev1.ClaimResourceStatus{
						corev1.ResourceStorage: corev1.PersistentVolumeClaimNodeResizePending,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := pvcSize(tt.pvc)
			if size == nil {
				t.Fatal("pvcSize returned nil, callers dereference it")
			}
			if !size.IsZero() {
				t.Fatalf("pvcSize = %s, want zero for a claim with no capacity", size)
			}
			if size.Cmp(requested) == 0 {
				t.Fatal("a claim with no capacity must not count as resized")
			}
		})
	}
}

// Not every platform fills in allocatedResourceStatuses: it needs
// RecoverVolumeExpansionFailure, which is off on some supported Kubernetes
// versions. Where it is reported it wins, because it cannot outlive the resize
// that set it; where it is not, the sticky condition is all there is.
func TestPVCSizeSignals(t *testing.T) {
	const (
		old = "3Gi"
		new = "4Gi"
	)

	pvc := func(mutate func(*corev1.PersistentVolumeClaim)) corev1.PersistentVolumeClaim {
		p := corev1.PersistentVolumeClaim{
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(new)},
				},
			},
			Status: corev1.PersistentVolumeClaimStatus{
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(old)},
			},
		}
		mutate(&p)
		return p
	}

	withStatus := func(s corev1.ClaimResourceStatus, allocated string) func(*corev1.PersistentVolumeClaim) {
		return func(p *corev1.PersistentVolumeClaim) {
			p.Status.AllocatedResourceStatuses = map[corev1.ResourceName]corev1.ClaimResourceStatus{
				corev1.ResourceStorage: s,
			}
			p.Status.AllocatedResources = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(allocated)}
		}
	}

	withCondition := func(p *corev1.PersistentVolumeClaim) {
		p.Status.Conditions = append(p.Status.Conditions, corev1.PersistentVolumeClaimCondition{
			Type:   corev1.PersistentVolumeClaimFileSystemResizePending,
			Status: corev1.ConditionTrue,
		})
	}

	tests := []struct {
		name string
		pvc  corev1.PersistentVolumeClaim
		want string
	}{
		{
			name: "the volume is expanded and the node resize is pending",
			pvc:  pvc(withStatus(corev1.PersistentVolumeClaimNodeResizePending, new)),
			want: new,
		},
		{
			name: "the volume is still being expanded",
			pvc:  pvc(withStatus(corev1.PersistentVolumeClaimControllerResizeInProgress, new)),
			want: old,
		},
		{
			// a condition left over from an earlier expansion must never stand in
			// for the current one, or a replica starts on an undersized volume
			name: "a leftover condition is ignored when the status is reported",
			pvc: pvc(func(p *corev1.PersistentVolumeClaim) {
				withStatus(corev1.PersistentVolumeClaimControllerResizeInProgress, old)(p)
				withCondition(p)
			}),
			want: old,
		},
		{
			// the only signal such a platform gives: it may be left over from an
			// earlier expansion, which the next resize corrects once the volume is
			// mounted and reports its capacity again
			name: "the condition is used when no status is reported",
			pvc:  pvc(withCondition),
			want: new,
		},
		{
			// nothing to expand yet: it is created at the size it asks for
			name: "an unbound claim reports the size it asks for",
			pvc: pvc(func(p *corev1.PersistentVolumeClaim) {
				p.Status.Capacity = nil
			}),
			want: new,
		},
		{
			name: "no status and no condition means nothing happened yet",
			pvc:  pvc(func(*corev1.PersistentVolumeClaim) {}),
			want: old,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := resource.MustParse(tt.want)
			got := pvcSize(tt.pvc)
			if got.Cmp(want) != 0 {
				t.Fatalf("pvcSize = %s, want %s", got, &want)
			}
		})
	}
}

// Repeated events are coalesced into one object whose first timestamp stays put
// while the last one moves, and events written through the core API carry no
// eventTime at all.
func TestLastSeen(t *testing.T) {
	first := time.Date(2026, 8, 22, 17, 19, 44, 0, time.UTC)
	last := time.Date(2026, 8, 22, 17, 25, 19, 0, time.UTC)

	tests := []struct {
		name  string
		event eventsv1.Event
		want  time.Time
	}{
		{
			name: "coalesced event with no eventTime",
			event: eventsv1.Event{
				DeprecatedFirstTimestamp: metav1.NewTime(first),
				DeprecatedLastTimestamp:  metav1.NewTime(last),
			},
			want: last,
		},
		{
			name:  "a fresh event carries its own time",
			event: eventsv1.Event{EventTime: metav1.NewMicroTime(last)},
			want:  last,
		},
		{
			name: "a series reports when it was last observed",
			event: eventsv1.Event{
				DeprecatedFirstTimestamp: metav1.NewTime(first),
				Series:                   &eventsv1.EventSeries{LastObservedTime: metav1.NewMicroTime(last)},
			},
			want: last,
		},
		{
			name:  "nothing but a creation timestamp",
			event: eventsv1.Event{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(first)}},
			want:  first,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastSeen(tt.event); !got.Equal(tt.want) {
				t.Fatalf("lastSeen = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestStashAppliedConfig(t *testing.T) {
	const (
		crName  = "cluster1"
		ns      = "stash-ns"
		applied = `{"max_connections":"200"}`
	)

	newCR := func() *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
		}
	}

	newSTS := func(annotations map[string]string) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        crName + "-mysql",
				Namespace:   ns,
				Annotations: annotations,
			},
		}
	}

	tests := map[string]struct {
		sts  *appsv1.StatefulSet
		want string // empty means the cr must be left without the annotation
	}{
		"a recorded config is copied to the cr": {
			sts:  newSTS(map[string]string{naming.AnnotationLastAppliedConfig.String(): applied}),
			want: applied,
		},
		"a set with no record leaves the cr alone": {
			sts: newSTS(map[string]string{"other": "value"}),
		},
		"a set with no annotations at all leaves the cr alone": {
			sts: newSTS(nil),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			cr := newCR()
			cl := fake.NewClientBuilder().
				WithScheme(newScheme(t)).
				WithObjects(cr, tc.sts).
				Build()
			r := &PerconaServerMySQLReconciler{Client: cl, Scheme: cl.Scheme()}

			require.NoError(t, r.stashAppliedConfig(ctx, cr, tc.sts))

			updated := new(apiv1.PerconaServerMySQL)
			require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: crName, Namespace: ns}, updated))

			got, ok := updated.GetAnnotations()[naming.AnnotationLastAppliedConfig.String()]
			if tc.want == "" {
				assert.False(t, ok, "nothing to stash must not annotate the cr")
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
