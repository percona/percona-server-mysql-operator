package ps

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		name    string
		pvc     corev1.PersistentVolumeClaim
		mounted bool
	}{
		{
			name:    "pending claim with a pod",
			pvc:     corev1.PersistentVolumeClaim{},
			mounted: true,
		},
		{
			name:    "pending claim without a pod",
			pvc:     corev1.PersistentVolumeClaim{},
			mounted: false,
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
			mounted: true,
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
			mounted: false,
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
			mounted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			size := pvcSize(tt.pvc, tt.mounted)
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
// versions. Where it is reported it must be used, because it cannot outlive the
// resize that set it, and where it is not the condition has to do.
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
		name    string
		pvc     corev1.PersistentVolumeClaim
		mounted bool
		want    string
	}{
		{
			name:    "a mounted claim reports its own capacity",
			pvc:     pvc(withStatus(corev1.PersistentVolumeClaimNodeResizePending, new)),
			mounted: true,
			want:    old,
		},
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
			// the condition must not be trusted where the status is reported,
			// since it can be left over from an earlier expansion
			name: "a leftover condition is ignored when the status is reported",
			pvc: pvc(func(p *corev1.PersistentVolumeClaim) {
				withStatus(corev1.PersistentVolumeClaimControllerResizeInProgress, old)(p)
				withCondition(p)
			}),
			want: old,
		},
		{
			// platforms that do not report the status at all
			name: "the condition is used when no status is reported",
			pvc:  pvc(withCondition),
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
			got := pvcSize(tt.pvc, tt.mounted)
			if got.Cmp(want) != 0 {
				t.Fatalf("pvcSize = %s, want %s", got, &want)
			}
		})
	}
}
