package ps

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"hash/crc32"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/percona/percona-server-mysql-operator/pkg/util"
	"github.com/pkg/errors"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

type backupScheduleJob struct {
	apiv1alpha1.BackupSchedule
	scheduleJob
}

func (r *CronRegistry) deleteBackupJob(name string) {
	job, ok := r.backupJobs.LoadAndDelete(name)
	if !ok {
		return
	}
	r.crons.Remove(job.(backupScheduleJob).jobID)
}

func (r *CronRegistry) stopBackupJob(name string) {
	job, ok := r.backupJobs.Load(name)
	if !ok {
		return
	}
	r.crons.Remove(job.(backupScheduleJob).jobID)
}

func (r *CronRegistry) getBackupJob(bcp apiv1alpha1.BackupSchedule) backupScheduleJob {
	sch := backupScheduleJob{}
	schRaw, ok := r.backupJobs.Load(bcp.Name)
	if ok {
		sch = schRaw.(backupScheduleJob)
	}
	return sch
}

func (r *CronRegistry) addBackupJob(ctx context.Context, cl client.Client, cluster *apiv1alpha1.PerconaServerMySQL, bcp apiv1alpha1.BackupSchedule) error {
	if bcp.Schedule == "" {
		r.stopBackupJob(bcp.Name)
		return nil
	}
	r.deleteBackupJob(bcp.Name)
	jobID, err := r.addFuncWithSeconds(bcp.Schedule, r.createBackupJobFunc(ctx, cl, cluster, bcp))
	if err != nil {
		return errors.Wrap(err, "add func")
	}

	r.backupJobs.Store(bcp.Name, backupScheduleJob{
		BackupSchedule: bcp,
		scheduleJob:    scheduleJob{jobID: jobID},
	})
	return nil
}

func (r *CronRegistry) createBackupJobFunc(ctx context.Context, cl client.Client, cluster *apiv1alpha1.PerconaServerMySQL, backupJob apiv1alpha1.BackupSchedule) func() {
	log := logf.FromContext(ctx)

	return func() {
		cr := &apiv1alpha1.PerconaServerMySQL{}
		err := cl.Get(ctx, types.NamespacedName{Name: cluster.Name, Namespace: cluster.Namespace}, cr)
		if err != nil {
			if k8serrors.IsNotFound(err) {
				log.Info("Cluster is not found. Deleting the job", "name", backupJob.Name, "cluster", cluster.Name, "namespace", cluster.Namespace)
				r.deleteBackupJob(backupJob.Name)
				return
			}
			log.Error(err, "failed to get cluster")
		}

		bcp := &apiv1alpha1.PerconaServerMySQLBackup{
			ObjectMeta: metav1.ObjectMeta{
				Finalizers: []string{naming.FinalizerDeleteBackup},
				Namespace:  cr.Namespace,
				Name:       generateBackupName(cr, backupJob),
				Labels: util.SSMapMerge(map[string]string{
					naming.LabelBackupAncestor: backupJob.Name,
					naming.LabelCluster:        cr.Name,
					naming.LabelBackupType:     "cron",
				}, naming.Labels("percona-server-backup", "", "percona-server", "")),
			},
			Spec: apiv1alpha1.PerconaServerMySQLBackupSpec{
				ClusterName: cr.Name,
				StorageName: backupJob.StorageName,
			},
		}

		err = cl.Create(ctx, bcp)
		if err != nil {
			log.Error(err, "failed to create backup")
		}
	}
}

func (r *PerconaServerMySQLReconciler) reconcileScheduledBackup(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileScheduledBackup")

	backups := make(map[string]apiv1alpha1.BackupSchedule)
	backupNamePrefix := backupJobClusterPrefix(cr.Namespace + "-" + cr.Name)

	for i, bcp := range cr.Spec.Backup.Schedule {
		_, ok := cr.Spec.Backup.Storages[bcp.StorageName]
		if !ok {
			log.Info("Invalid storage name for backup", "backup name", cr.Spec.Backup.Schedule[i].Name, "storage name", bcp.StorageName)
			continue
		}

		bcp.Name = backupNamePrefix + "-" + bcp.Name
		backups[bcp.Name] = bcp

		sch := r.Crons.getBackupJob(bcp)
		if ok && sch.Schedule == bcp.Schedule && sch.StorageName == bcp.StorageName {
			continue
		}

		log.Info("Creating or updating backup job", "name", bcp.Name, "schedule", bcp.Schedule)
		if err := r.Crons.addBackupJob(ctx, r.Client, cr, bcp); err != nil {
			log.Error(err, "can't add backup job", "backup name", cr.Spec.Backup.Schedule[i].Name, "schedule", bcp.Schedule)
		}
	}

	r.Crons.backupJobs.Range(func(k, v interface{}) bool {
		item := v.(backupScheduleJob)
		if !strings.HasPrefix(item.Name, backupNamePrefix) {
			return true
		}

		spec, ok := backups[item.Name]
		if !ok {
			log.Info("Deleting outdated backup job", "name", item.Name)
			r.Crons.deleteBackupJob(item.Name)
			return true
		}

		if spec.Keep <= 0 {
			return true
		}

		oldBackups, err := r.oldScheduledBackups(ctx, cr, item.Name, spec.Keep)
		if err != nil {
			log.Error(err, "failed to list old backups", "name", item.Name)
			return true
		}

		for _, bcp := range oldBackups {
			err = r.Delete(ctx, &bcp)
			if err != nil {
				log.Error(err, "failed to delete old backup", "name", bcp.Name)
			}
		}

		return true
	})

	return nil
}

func backupJobClusterPrefix(clusterName string) string {
	h := sha1.New()
	h.Write([]byte(clusterName))
	return hex.EncodeToString(h.Sum(nil))[:5]
}

func (r *PerconaServerMySQLReconciler) oldScheduledBackups(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL, ancestor string, keep int) ([]apiv1alpha1.PerconaServerMySQLBackup, error) {
	bcpList := apiv1alpha1.PerconaServerMySQLBackupList{}
	err := r.List(ctx,
		&bcpList,
		&client.ListOptions{
			Namespace: cr.Namespace,
			LabelSelector: labels.SelectorFromSet(map[string]string{
				naming.LabelCluster:        cr.Name,
				naming.LabelBackupAncestor: ancestor,
			}),
		},
	)
	if err != nil {
		return []apiv1alpha1.PerconaServerMySQLBackup{}, err
	}

	if len(bcpList.Items) <= keep {
		return []apiv1alpha1.PerconaServerMySQLBackup{}, nil
	}

	backups := []apiv1alpha1.PerconaServerMySQLBackup{}
	for _, bcp := range bcpList.Items {
		if bcp.Status.State == apiv1alpha1.BackupSucceeded {
			backups = append(backups, bcp)
		}
	}

	if len(backups) <= keep {
		return []apiv1alpha1.PerconaServerMySQLBackup{}, nil
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreationTimestamp.Compare(backups[j].CreationTimestamp.Time) == -1
	})

	backups = backups[:len(backups)-keep]

	return backups, nil
}

func generateBackupName(cr *apiv1alpha1.PerconaServerMySQL, backupJob apiv1alpha1.BackupSchedule) string {
	result := "cron-"
	if len(cr.Name) > 16 {
		result += cr.Name[:16]
	} else {
		result += cr.Name
	}
	storageName := backupJob.StorageName
	if len(storageName) > 16 {
		storageName = storageName[:16]
	}
	result += "-" + storageName + "-"

	tnow := time.Now()
	result += tnow.Format("20060102150405")

	result += "-" + strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(backupJob.Schedule))), 32)[:5]

	return result
}
