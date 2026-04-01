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
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
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

func newBackupWithMeta(name, clusterName string, backupType apiv1.BackupType,
	state apiv1.BackupState, completedAt *metav1.Time, dest apiv1.BackupDestination,
	annotations map[string]string) *apiv1.PerconaServerMySQLBackup {
	b := newBackup(name, clusterName, backupType, state, completedAt)
	b.Status.Destination = dest
	if annotations != nil {
		b.Annotations = annotations
	}
	return b
}

func TestGetLatestIncrementalBackupInChain(t *testing.T) {
	now := time.Now()
	baseAnno := string(naming.AnnotationBaseBackupName)

	tests := map[string]struct {
		inputBackup *apiv1.PerconaServerMySQLBackup
		listBackups []*apiv1.PerconaServerMySQLBackup
		wantName    string
		wantErr     string
	}{
		"full backup input returns latest incremental in chain": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-3*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-old", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-2*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
				newBackupWithMeta("incr-new", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-1*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "incr-new",
		},
		"incremental backup input returns latest incremental in chain": {
			inputBackup: newBackupWithMeta("incr-input", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-older", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-3*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
				newBackupWithMeta("incr-newest", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-1*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "incr-newest",
		},
		"single incremental in chain": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("only-incr", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-1*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "only-incr",
		},
		"skips full backups in list": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-3*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("full-2", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
					metaTime(now), "s3://bucket/prefix/full-2", nil),
				newBackupWithMeta("incr-1", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-1*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "incr-1",
		},
		"skips non-succeeded incrementals": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-3*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-running", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupRunning,
					metaTime(now), "", map[string]string{baseAnno: "full-1"}),
				newBackupWithMeta("incr-failed", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupFailed,
					metaTime(now), "", map[string]string{baseAnno: "full-1"}),
				newBackupWithMeta("incr-ok", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-2*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "incr-ok",
		},
		"skips incrementals pointing to different base": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-3*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-other", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now), "", map[string]string{baseAnno: "full-2"}),
				newBackupWithMeta("incr-mine", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now.Add(-1*time.Hour)), "", map[string]string{baseAnno: "full-1"}),
			},
			wantName: "incr-mine",
		},
		"error when input incremental has no base annotation": {
			inputBackup: newBackupWithMeta("incr-no-anno", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
				metaTime(now), "", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-1", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now), "", map[string]string{baseAnno: "full-1"}),
			},
			wantErr: "base backup name not known in annotations",
		},
		"error when listed incremental backup has no annotation": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-no-anno", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					metaTime(now), "", nil),
			},
			wantErr: "base backup name not known in annotations",
		},
		"error when completedAt is zero": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("incr-zero-time", "cluster1", apiv1.BackupTypeIncremental, apiv1.BackupSucceeded,
					&metav1.Time{}, "", map[string]string{baseAnno: "full-1"}),
			},
			wantErr: "completedAt is not set",
		},
		"error when no incremental backups exist": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "s3://bucket/prefix/full-1", nil),
			listBackups: []*apiv1.PerconaServerMySQLBackup{
				newBackupWithMeta("full-2", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
					metaTime(now), "s3://bucket/prefix/full-2", nil),
			},
			wantErr: "no incremental backup found in chain",
		},
		"error when no backups exist at all": {
			inputBackup: newBackupWithMeta("full-1", "cluster1", apiv1.BackupTypeFull, apiv1.BackupSucceeded,
				metaTime(now.Add(-2*time.Hour)), "s3://bucket/prefix/full-1", nil),
			wantErr: "no incremental backup found in chain",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var objs []client.Object
			objs = append(objs, tt.inputBackup)
			for _, b := range tt.listBackups {
				objs = append(objs, b)
			}

			cl := buildTestClient(objs...)
			result, err := GetLatestIncrementalBackupInChain(context.Background(), cl, tt.inputBackup)

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
