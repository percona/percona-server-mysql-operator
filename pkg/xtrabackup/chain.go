package xtrabackup

import (
	"context"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type BackupChain struct {
	backups []apiv1.PerconaServerMySQLBackup
}

func NewBackupChain(ctx context.Context, cl client.Client, chainName, clusterName string) (*BackupChain, error) {
	if chainName == "" {
		return nil, errors.New("chain name is required")
	}
	if clusterName == "" {
		return nil, errors.New("cluster name is required")
	}

	// List backups belonging to this chain and cluster.
	backupList := &apiv1.PerconaServerMySQLBackupList{}
	listOpts := []client.ListOption{
		client.MatchingFields{
			"spec.clusterName": clusterName,
		},
		client.MatchingLabels{
			naming.LabelBackupChain: chainName,
		},
	}
	if err := cl.List(ctx, backupList, listOpts...); err != nil {
		return nil, errors.Wrap(err, "list backups")
	}

	// Separate base (full) backup from incremental backups.
	var baseBackup *apiv1.PerconaServerMySQLBackup
	incrementals := make(map[string]apiv1.PerconaServerMySQLBackup)
	for i, backup := range backupList.Items {
		if backup.Spec.Type == apiv1.BackupTypeFull {
			if baseBackup != nil {
				return nil, errors.New("multiple full backups found for chain")
			}
			baseBackup = &backupList.Items[i]
			continue
		}
		if backup.Status.State != apiv1.BackupSucceeded {
			continue
		}
		if backup.Status.PreviousBackupName == nil {
			continue
		}
		prev := *backup.Status.PreviousBackupName
		if _, exists := incrementals[prev]; exists {
			return nil, errors.Errorf("multiple incremental backups reference the same previous backup %q", prev)
		}
		incrementals[prev] = backupList.Items[i]
	}

	if baseBackup == nil {
		return nil, errors.New("no full backup found for chain")
	}
	if baseBackup.Status.State != apiv1.BackupSucceeded {
		return nil, errors.Errorf("base backup %q is not succeeded (state: %s)", baseBackup.Name, baseBackup.Status.State)
	}

	// Build the ordered chain by following PreviousBackupName links from the base.
	chain := []apiv1.PerconaServerMySQLBackup{*baseBackup}
	current := baseBackup.Name
	for {
		next, ok := incrementals[current]
		if !ok {
			break
		}
		prev := chain[len(chain)-1]
		if next.Status.CompletedAt != nil && prev.Status.CompletedAt != nil &&
			!next.Status.CompletedAt.After(prev.Status.CompletedAt.Time) {
			return nil, errors.Errorf("backup %q (completed %v) is not after previous backup %q (completed %v)",
				next.Name, next.Status.CompletedAt.Time, prev.Name, prev.Status.CompletedAt.Time)
		}
		chain = append(chain, next)
		current = next.Name
	}

	return &BackupChain{backups: chain}, nil
}

// Head returns the first backup in the chain, which is the base backup.
// All other backups are incremental backups.
func (c *BackupChain) Head() apiv1.PerconaServerMySQLBackup {
	return c.backups[0]
}

// Tail returns the last incremental backup in the chain.:w
func (c *BackupChain) Tail() apiv1.PerconaServerMySQLBackup {
	return c.backups[len(c.backups)-1]
}

// ListAll returns a copy of all backups in the chain.
func (c *BackupChain) ListAll() []apiv1.PerconaServerMySQLBackup {
	var cpy []apiv1.PerconaServerMySQLBackup
	copy(cpy, c.backups)
	return cpy
}
