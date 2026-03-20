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
	for _, backup := range backupList.Items {
		if backup.Spec.Type != apiv1.BackupTypeFull {
			continue
		}

		if lastFullBackup == nil {
			lastFullBackup = &backup
			continue
		}
		if backup.Status.CompletedAt.After(lastFullBackup.Status.CompletedAt.Time) {
			lastFullBackup = &backup
		}
	}

	if lastFullBackup == nil {
		return nil, errors.New("no full backup found")
	}
	return lastFullBackup, nil
}
