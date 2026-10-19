package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	coordv1 "k8s.io/api/coordination/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestCleanupLeaderElectionLease(t *testing.T) {
	deleteErr := errors.New("lease deletion failed")
	tests := []struct {
		name            string
		leaseName       string
		leaderElection  bool
		leaseExists     bool
		deleteError     error
		wantError       error
		wantDeleteCalls int
		wantLeaseExists bool
	}{
		{
			name:            "deletes the default lease",
			leaseName:       defaultElectionID,
			leaseExists:     true,
			wantDeleteCalls: 1,
		},
		{
			name:            "deletes a custom lease",
			leaseName:       "custom.lease",
			leaseExists:     true,
			wantDeleteCalls: 1,
		},
		{
			name:            "skips deletion when leader election is enabled",
			leaseName:       defaultElectionID,
			leaderElection:  true,
			leaseExists:     true,
			wantDeleteCalls: 0,
			wantLeaseExists: true,
		},
		{
			name:            "ignores a missing lease",
			leaseName:       defaultElectionID,
			wantDeleteCalls: 1,
		},
		{
			name:            "returns deletion errors",
			leaseName:       defaultElectionID,
			leaseExists:     true,
			deleteError:     deleteErr,
			wantError:       deleteErr,
			wantDeleteCalls: 1,
			wantLeaseExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := ctrl.Options{
				LeaderElection:          tt.leaderElection,
				LeaderElectionID:        tt.leaseName,
				LeaderElectionNamespace: "operator-ns",
			}
			lease := &coordv1.Lease{Name: tt.leaseName, Namespace: "operator-ns"}
			otherLease := &coordv1.Lease{Name: "other-lease", Namespace: "operator-ns"}
			otherNamespaceLease := &coordv1.Lease{Name: tt.leaseName, Namespace: "other-ns"}
			builder := fake.NewClientBuilder().WithScheme(scheme).
				WithObjects(otherLease, otherNamespaceLease)
			if tt.leaseExists {
				builder.WithObjects(lease)
			}

			deleteCalls := 0
			cl := builder.WithInterceptorFuncs(interceptor.Funcs{
				Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					deleteCalls++
					if tt.deleteError != nil {
						return tt.deleteError
					}
					return cl.Delete(ctx, obj, opts...)
				},
			}).Build()

			err := cleanupLeaderElectionLease(t.Context(), cl, options)
			if tt.wantError != nil {
				require.ErrorIs(t, err, tt.wantError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.wantDeleteCalls, deleteCalls, "delete calls")

			err = cl.Get(t.Context(), client.ObjectKeyFromObject(lease), new(coordv1.Lease))
			if tt.wantLeaseExists {
				require.NoError(t, err, "configured lease should remain")
			} else {
				require.True(t, k8serrors.IsNotFound(err), "configured lease should be absent, got %v", err)
			}
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(otherLease), new(coordv1.Lease)), "unrelated lease should remain")
			require.NoError(t, cl.Get(t.Context(), client.ObjectKeyFromObject(otherNamespaceLease), new(coordv1.Lease)), "lease in another namespace should remain")
		})
	}
}
