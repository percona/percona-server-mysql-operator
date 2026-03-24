package k8s

import (
	"context"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func GetLastFullBackup(
	ctx context.Context,
	cl client.Client,
	clusterName string,
) (*apiv1.PerconaServerMySQLBackup, error) {
	backupList := &apiv1.PerconaServerMySQLBackupList{}
	if err := cl.List(ctx, backupList, client.MatchingFields{
		"spec.clusterName": clusterName,
	}); err != nil {
		return nil, errors.Wrap(err, "list backups")
	}

	var lastFullBackup *apiv1.PerconaServerMySQLBackup
	for i, backup := range backupList.Items {
		backupType := backup.Spec.Type
		if backupType == "" {
			backupType = apiv1.BackupTypeFull
		}
		if backupType != apiv1.BackupTypeFull {
			continue
		}

		if backup.Status.State != apiv1.BackupSucceeded {
			continue
		}

		if backup.Status.CompletedAt == nil {
			return nil, errors.Errorf("backup '%s' is succeeded but completedAt is not set", backup.GetName())
		}

		if lastFullBackup == nil {
			lastFullBackup = &backupList.Items[i]
			continue
		}

		if backup.Status.CompletedAt.After(lastFullBackup.Status.CompletedAt.Time) {
			lastFullBackup = &backupList.Items[i]
		}
	}

	if lastFullBackup == nil {
		return nil, errors.New("no full backup found")
	}
	return lastFullBackup, nil
}

func GetLastSuccessfulBackup(
	ctx context.Context,
	cl client.Client,
	clusterName string,
) (*apiv1.PerconaServerMySQLBackup, error) {
	backupList := &apiv1.PerconaServerMySQLBackupList{}
	if err := cl.List(ctx, backupList, client.MatchingFields{
		"spec.clusterName": clusterName,
	}); err != nil {
		return nil, errors.Wrap(err, "list backups")
	}

	var lastFullBackup *apiv1.PerconaServerMySQLBackup
	for i, backup := range backupList.Items {
		if backup.Status.State != apiv1.BackupSucceeded {
			continue
		}

		if backup.Status.CompletedAt == nil {
			return nil, errors.Errorf("backup '%s' is succeeded but completedAt is not set", backup.GetName())
		}

		if lastFullBackup == nil {
			lastFullBackup = &backupList.Items[i]
			continue
		}

		if backup.Status.CompletedAt.After(lastFullBackup.Status.CompletedAt.Time) {
			lastFullBackup = &backupList.Items[i]
		}
	}

	if lastFullBackup == nil {
		return nil, errors.New("no previous backup found")
	}
	return lastFullBackup, nil
}
