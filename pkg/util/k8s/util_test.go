package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func buildTestClient(objs ...client.Object) client.Client {
	s := scheme.Scheme
	s.AddKnownTypes(apiv1.GroupVersion,
		new(apiv1.PerconaServerMySQLBackup),
		new(apiv1.PerconaServerMySQLBackupList),
	)

	return fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithIndex(&apiv1.PerconaServerMySQLBackup{}, "spec.clusterName", func(o client.Object) []string {
			b, ok := o.(*apiv1.PerconaServerMySQLBackup)
			if !ok {
				return nil
			}
			return []string{b.Spec.ClusterName}
		}).
		Build()
}

func newBackup(name, clusterName string, backupType apiv1.BackupType, state apiv1.BackupState, completedAt *metav1.Time) *apiv1.PerconaServerMySQLBackup {
	return &apiv1.PerconaServerMySQLBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: apiv1.PerconaServerMySQLBackupSpec{
			Type:        backupType,
			ClusterName: clusterName,
		},
		Status: apiv1.PerconaServerMySQLBackupStatus{
			State:       state,
			CompletedAt: completedAt,
		},
	}
}

func metaTime(t time.Time) *metav1.Time {
	return &metav1.Time{Time: t}
}

func TestGetLastFullBackup(t *testing.T) {
	now := time.Now()

	tests := map[string]struct {
		backups     []*apiv1.PerconaServerMySQLBackup
		clusterName string
		wantName    string
		wantErr     string
	}{
		"returns latest full backup among multiple": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("older-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
				newBackup("newer-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "newer-full",
		},
		"returns latest full backup even when older than incremental": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("old-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-3*time.Hour))),
				newBackup("new-incr", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "old-full",
		},
		"returns full backup when newer than incremental": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("new-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
				newBackup("old-incr", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded, metaTime(now.Add(-3*time.Hour))),
			},
			wantName: "new-full",
		},
		"skips non-succeeded backups": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("running", "cluster1", apiv1.BackupTypeFull, apiv1.BackupRunning, metaTime(now)),
				newBackup("failed", "cluster1", apiv1.BackupTypeFull, apiv1.BackupFailed, metaTime(now)),
				newBackup("succeeded", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
			},
			wantName: "succeeded",
		},
		"unknown type is treated as full": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("older-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
				newBackup("newer-full", "cluster1", "", apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "newer-full",
		},
		"error when no backups exist": {
			clusterName: "cluster1",
			backups:     nil,
			wantErr:     "no full backup found",
		},
		"error when only non-succeeded backups exist": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("running", "cluster1", apiv1.BackupTypeFull, apiv1.BackupRunning, metaTime(now)),
				newBackup("error", "cluster1", apiv1.BackupTypeFull, apiv1.BackupError, nil),
			},
			wantErr: "no full backup found",
		},
		"error when succeeded backup has nil completedAt": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("bad-backup", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, nil),
			},
			wantErr: "backup 'bad-backup' is succeeded but completedAt is not set",
		},
		"filters by cluster name": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("cluster2-backup", "cluster2", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now)),
				newBackup("cluster1-backup", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "cluster1-backup",
		},
		"single succeeded full backup": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("only-one", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now)),
			},
			wantName: "only-one",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var objs []client.Object
			for _, b := range tt.backups {
				objs = append(objs, b)
			}

			cl := buildTestClient(objs...)
			result, err := GetLastFullBackup(context.Background(), cl, tt.clusterName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantName, result.Name)
		})
	}
}

func TestGetLastSuccessfulBackup(t *testing.T) {
	now := time.Now()

	tests := map[string]struct {
		backups     []*apiv1.PerconaServerMySQLBackup
		clusterName string
		wantName    string
		wantErr     string
	}{
		"returns latest succeeded backup among multiple": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("older", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
				newBackup("newer", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "newer",
		},
		"returns incremental when it is the latest": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("old-full", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-3*time.Hour))),
				newBackup("new-incr", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "new-incr",
		},
		"skips non-succeeded backups": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("running", "cluster1", apiv1.BackupTypeFull, apiv1.BackupRunning, metaTime(now)),
				newBackup("failed", "cluster1", apiv1.BackupTypeFull, apiv1.BackupFailed, metaTime(now)),
				newBackup("starting", "cluster1", apiv1.BackupTypeFull, apiv1.BackupStarting, metaTime(now)),
				newBackup("succeeded", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
			},
			wantName: "succeeded",
		},
		"error when no backups exist": {
			clusterName: "cluster1",
			backups:     nil,
			wantErr:     "no previous backup found",
		},
		"error when only non-succeeded backups exist": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("running", "cluster1", apiv1.BackupTypeFull, apiv1.BackupRunning, metaTime(now)),
				newBackup("error", "cluster1", apiv1.BackupTypeFull, apiv1.BackupError, nil),
			},
			wantErr: "no previous backup found",
		},
		"error when succeeded backup has nil completedAt": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("bad-backup", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, nil),
			},
			wantErr: "backup 'bad-backup' is succeeded but completedAt is not set",
		},
		"nil completedAt error even with valid backups present": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("good", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-2*time.Hour))),
				newBackup("bad", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, nil),
			},
			wantErr: "backup 'bad' is succeeded but completedAt is not set",
		},
		"filters by cluster name": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("other-cluster", "cluster2", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now)),
				newBackup("my-cluster", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now.Add(-1*time.Hour))),
			},
			wantName: "my-cluster",
		},
		"single succeeded backup": {
			clusterName: "cluster1",
			backups: []*apiv1.PerconaServerMySQLBackup{
				newBackup("only-one", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded, metaTime(now)),
			},
			wantName: "only-one",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var objs []client.Object
			for _, b := range tt.backups {
				objs = append(objs, b)
			}

			cl := buildTestClient(objs...)
			result, err := GetLastSuccessfulBackup(context.Background(), cl, tt.clusterName)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantName, result.Name)
		})
	}
}
