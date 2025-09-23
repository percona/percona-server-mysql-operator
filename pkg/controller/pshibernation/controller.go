/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pshibernation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/robfig/cron/v3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sretry "k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

// PerconaServerMySQLHibernationReconciler reconciles PerconaServerMySQL hibernation
type PerconaServerMySQLHibernationReconciler struct {
	client.Client
	Scheme        *runtime.Scheme
	ServerVersion *platform.ServerVersion
}

//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqls,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlbackups,verbs=get;list;watch
//+kubebuilder:rbac:groups=ps.percona.com,resources=perconaservermysqlrestores,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *PerconaServerMySQLHibernationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithName("pshibernation-controller")

	// Fetch the PerconaServerMySQL instance
	cr := &apiv1.PerconaServerMySQL{}
	if err := r.Client.Get(ctx, req.NamespacedName, cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check if hibernation is enabled
	if !cr.IsHibernationEnabled() {
		// Only update status if it's not already disabled to avoid log spam
		if cr.Status.Hibernation == nil || cr.Status.Hibernation.State != apiv1.HibernationStateDisabled {
			if err := r.updateHibernationState(ctx, cr, apiv1.HibernationStateDisabled, ""); err != nil {
				log.Error(err, "Failed to update hibernation state to disabled", "cluster", cr.Name, "namespace", cr.Namespace)
			}
		}
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Synchronize hibernation state with actual cluster state
	if err := r.synchronizeHibernationState(ctx, cr); err != nil {
		log.Error(err, "Failed to synchronize hibernation state", "cluster", cr.Name, "namespace", cr.Namespace)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Skip hibernation processing if cluster is still initializing or in error state
	// This prevents hibernation state from flipping during cluster startup/recovery
	// BUT we need to proactively schedule for next window if the scheduled time is approaching or has passed
	if cr.Status.State == apiv1.StateInitializing || cr.Status.State == apiv1.StateError {
		// Proactively check and update nextPauseTime to next window if needed
		if err := r.proactivelyScheduleForNextWindow(ctx, cr); err != nil {
			log.Error(err, "Failed to proactively schedule for next window during cluster issues", "cluster", cr.Name, "namespace", cr.Namespace, "state", cr.Status.State)
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// When cluster becomes ready, check if we need to proactively schedule for next window
	// This handles the case where the cluster was unready during the scheduled time
	if cr.Status.State == apiv1.StateReady {
		if err := r.proactivelyScheduleForNextWindow(ctx, cr); err != nil {
			log.Error(err, "Failed to proactively schedule for next window when cluster became ready", "cluster", cr.Name, "namespace", cr.Namespace)
		}
	}

	// Process hibernation logic
	if err := r.processHibernation(ctx, cr); err != nil {
		log.Error(err, "Failed to process hibernation", "cluster", cr.Name, "namespace", cr.Namespace)
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, err
	}

	// Requeue after 1 minute to check again
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

// processHibernation handles the hibernation logic for a cluster
func (r *PerconaServerMySQLHibernationReconciler) processHibernation(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("processHibernation")
	now := time.Now()
	hibernation := cr.Spec.Hibernation

	// Check if it's time to pause
	if hibernation.Schedule.Pause != "" {
		if shouldPause, err := r.shouldPauseCluster(ctx, cr, hibernation.Schedule.Pause, now); err != nil {
			log.Error(err, "Failed to check pause schedule", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", hibernation.Schedule.Pause)
			return errors.Wrap(err, "failed to check pause schedule")
		} else {
			if shouldPause {
				if canPause, reason, err := r.canPauseCluster(ctx, cr); err != nil {
					log.Error(err, "Failed to check if cluster can be paused", "cluster", cr.Name, "namespace", cr.Namespace)
					return errors.Wrap(err, "failed to check if cluster can be paused")
				} else if canPause {
					if err := r.pauseCluster(ctx, cr); err != nil {
						log.Error(err, "Failed to pause cluster", "cluster", cr.Name, "namespace", cr.Namespace)
						return errors.Wrap(err, "failed to pause cluster")
					}
					log.Info("Cluster paused by hibernation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", hibernation.Schedule.Pause)
				} else {
					// Check if the reason is cluster not ready or active operations - if so, schedule for next window
					if strings.Contains(reason, "cluster not ready") || strings.Contains(reason, "active backup") || strings.Contains(reason, "active restore") {
						log.Info("Cluster not ready or active operations, scheduling hibernation for next window", "cluster", cr.Name, "namespace", cr.Namespace, "reason", reason, "schedule", hibernation.Schedule.Pause)
						if err := r.scheduleHibernationForNextWindow(ctx, cr, hibernation.Schedule.Pause, reason); err != nil {
							log.Error(err, "Failed to schedule hibernation for next window", "cluster", cr.Name, "namespace", cr.Namespace)
						}
					} else {
						log.Info("⚠️ Skipped pause due to other reasons", "cluster", cr.Name, "namespace", cr.Namespace, "reason", reason, "schedule", hibernation.Schedule.Pause)
						if err := r.updateHibernationState(ctx, cr, apiv1.HibernationStateBlocked, reason); err != nil {
							log.Error(err, "Failed to update hibernation status", "cluster", cr.Name, "namespace", cr.Namespace)
						}
					}
				}
			} else {
				// shouldPauseCluster returned false - check if we need to schedule for next window
				// This handles the case where the scheduled time has passed beyond the 5-minute window
				if err := r.checkAndScheduleForNextWindow(ctx, cr, hibernation.Schedule.Pause, now); err != nil {
					log.Error(err, "Failed to check and schedule for next window", "cluster", cr.Name, "namespace", cr.Namespace)
				}
			}
		}
	}

	// Check if it's time to unpause
	if hibernation.Schedule.Unpause != "" {
		if shouldUnpause, err := r.shouldUnpauseCluster(ctx, cr, hibernation.Schedule.Unpause, now); err != nil {
			log.Error(err, "Failed to check unpause schedule", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", hibernation.Schedule.Unpause)
			return errors.Wrap(err, "failed to check unpause schedule")
		} else {
			if shouldUnpause {
				if err := r.unpauseCluster(ctx, cr); err != nil {
					log.Error(err, "Failed to unpause cluster", "cluster", cr.Name, "namespace", cr.Namespace)
					return errors.Wrap(err, "failed to unpause cluster")
				}
				log.Info("Cluster unpaused by hibernation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", hibernation.Schedule.Unpause)
			}
		}
	}

	// Set appropriate state and calculate next times if hibernation status is not initialized
	// or if hibernation is enabled but state is still "Disabled"
	if cr.Status.Hibernation == nil || cr.Status.Hibernation.State == "" ||
		(cr.IsHibernationEnabled() && cr.Status.Hibernation.State == apiv1.HibernationStateDisabled) {

		// Log when hibernation gets enabled
		if cr.IsHibernationEnabled() && (cr.Status.Hibernation == nil || cr.Status.Hibernation.State == apiv1.HibernationStateDisabled) {
			pauseSchedule := "not set"
			unpauseSchedule := "not set"
			if cr.Spec.Hibernation.Schedule.Pause != "" {
				pauseSchedule = cr.Spec.Hibernation.Schedule.Pause
			}
			if cr.Spec.Hibernation.Schedule.Unpause != "" {
				unpauseSchedule = cr.Spec.Hibernation.Schedule.Unpause
			}
			log.Info("Hibernation enabled", "cluster", cr.Name, "namespace", cr.Namespace,
				"pauseSchedule", pauseSchedule, "unpauseSchedule", unpauseSchedule)
		}

		if err := r.initializeHibernationStatus(ctx, cr); err != nil {
			log.Error(err, "Failed to initialize hibernation status", "cluster", cr.Name, "namespace", cr.Namespace)
		}
	} else {
		// Check if hibernation schedule has changed and update next times if needed
		if err := r.updateHibernationScheduleIfChanged(ctx, cr); err != nil {
			log.Error(err, "Failed to update hibernation schedule", "cluster", cr.Name, "namespace", cr.Namespace)
		}
	}

	return nil
}

// scheduleHibernationForNextWindow schedules hibernation for the next available window when cluster is not ready
func (r *PerconaServerMySQLHibernationReconciler) scheduleHibernationForNextWindow(ctx context.Context, cr *apiv1.PerconaServerMySQL, schedule, reason string) error {
	log := logf.FromContext(ctx).WithName("scheduleHibernationForNextWindow")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy for next window scheduling", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Ensure hibernation status exists
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}

		// Parse the cron schedule to calculate next window
		cronSchedule, err := cron.ParseStandard(schedule)
		if err != nil {
			log.Error(err, "Failed to parse schedule for next window calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", schedule)
			return err
		}

		// Calculate next available window (tomorrow's schedule)
		now := time.Now().UTC()
		nextWindow := r.calculateNextScheduleTime(now, cronSchedule)

		// Update the next pause time to the next window
		fresh.Status.Hibernation.NextPauseTime = &nextWindow

		// Also update the unpause time to the next window if unpause schedule exists
		// BUT only if the cluster is not currently paused (to avoid overriding today's unpause time)
		if fresh.Spec.Hibernation != nil && fresh.Spec.Hibernation.Schedule.Unpause != "" && !fresh.Spec.Pause {
			if unpauseCronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Unpause); err == nil {
				nextUnpauseWindow := r.calculateNextScheduleTime(now, unpauseCronSchedule)
				fresh.Status.Hibernation.NextUnpauseTime = &nextUnpauseWindow
				log.Info("Also updated next unpause time for next window", "cluster", cr.Name, "namespace", cr.Namespace, "nextUnpauseWindow", nextUnpauseWindow)
			}
		}

		// Set state to indicate we're waiting for next window
		fresh.Status.Hibernation.State = apiv1.HibernationStateScheduled
		fresh.Status.Hibernation.Reason = fmt.Sprintf("Scheduled for next window: %s", reason)

		// Update the status
		if err := r.Client.Status().Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update hibernation status for next window", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		log.Info("Hibernation scheduled for next window", "cluster", cr.Name, "namespace", cr.Namespace,
			"nextWindow", nextWindow, "reason", reason)

		return nil
	})
}

// synchronizeHibernationState synchronizes the hibernation state with the actual cluster state
func (r *PerconaServerMySQLHibernationReconciler) synchronizeHibernationState(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("synchronizeHibernationState")

	// Get fresh copy of the cluster to check current state
	fresh := &apiv1.PerconaServerMySQL{}
	if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
		return err
	}

	// Ensure hibernation status exists
	if fresh.Status.Hibernation == nil {
		fresh.Status.Hibernation = &apiv1.HibernationStatus{}
	}

	// Check if the cluster is actually paused by looking at the cluster state
	// A cluster is considered "paused" if it's in StatePaused or StateStopping
	isClusterPaused := fresh.Status.State == apiv1.StatePaused || fresh.Status.State == apiv1.StateStopping
	currentHibernationState := fresh.Status.Hibernation.State

	// Determine what the hibernation state should be
	var expectedState string
	if isClusterPaused {
		expectedState = apiv1.HibernationStatePaused
	} else {
		// If hibernation state is Scheduled, preserve it - don't change to Active
		if currentHibernationState == apiv1.HibernationStateScheduled {
			expectedState = apiv1.HibernationStateScheduled
		} else {
			expectedState = apiv1.HibernationStateActive
		}
	}

	// Update hibernation state if it doesn't match the actual cluster state
	if currentHibernationState != expectedState {
		log.Info("Synchronizing hibernation state with cluster state",
			"cluster", cr.Name, "namespace", cr.Namespace,
			"clusterState", fresh.Status.State,
			"currentHibernationState", currentHibernationState,
			"expectedHibernationState", expectedState)

		if err := r.updateHibernationState(ctx, fresh, expectedState, ""); err != nil {
			return err
		}
	}

	return nil
}

// shouldPauseCluster checks if the cluster should be paused based on the cron schedule
func (r *PerconaServerMySQLHibernationReconciler) shouldPauseCluster(ctx context.Context, cr *apiv1.PerconaServerMySQL, schedule string, now time.Time) (bool, error) {
	log := logf.FromContext(ctx).WithName("shouldPauseCluster")

	// Parse cron schedule
	cronSchedule, err := cron.ParseStandard(schedule)
	if err != nil {
		log.Error(err, "Invalid pause schedule", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", schedule)
		return false, errors.Wrap(err, "invalid pause schedule")
	}

	// Check if cluster is already paused
	if cr.Spec.Pause {
		return false, nil
	}

	// Check if hibernation is scheduled for next window - if so, don't pause until that time
	if cr.Status.Hibernation != nil && cr.Status.Hibernation.State == apiv1.HibernationStateScheduled {
		// If we have a next pause time scheduled, only pause if that time has arrived
		if cr.Status.Hibernation.NextPauseTime != nil {
			if now.Before(cr.Status.Hibernation.NextPauseTime.Time) {
				log.Info("Hibernation scheduled for next window, waiting", "cluster", cr.Name, "namespace", cr.Namespace,
					"scheduledTime", cr.Status.Hibernation.NextPauseTime.Time, "currentTime", now)
				return false, nil
			}
			// The scheduled time has arrived, we can proceed with pausing
			log.Info("Scheduled hibernation time has arrived, proceeding with pause", "cluster", cr.Name, "namespace", cr.Namespace,
				"scheduledTime", cr.Status.Hibernation.NextPauseTime.Time, "currentTime", now)
		} else {
			// No next pause time set, but state is scheduled - this shouldn't happen, but be safe
			return false, nil
		}
	}

	// Get reference time for calculating next pause
	var referenceTime time.Time
	if cr.Status.Hibernation != nil && cr.Status.Hibernation.LastPauseTime != nil {
		// If we have a previous pause time, use it
		referenceTime = cr.Status.Hibernation.LastPauseTime.Time
	} else if cr.Status.Hibernation != nil && cr.Status.Hibernation.LastUnpauseTime != nil {
		// If no previous pause but we have an unpause time, use that
		referenceTime = cr.Status.Hibernation.LastUnpauseTime.Time
	} else {
		// If no previous times, this is first-time evaluation
		// For first-time evaluation, we should NOT pause if the scheduled time has already passed today
		// This prevents immediate pausing when hibernation is enabled after the scheduled time
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todaySchedule := cronSchedule.Next(today.Add(-time.Second)) // Get today's scheduled time

		// Check if the schedule actually applies to today (not tomorrow or later)
		isToday := todaySchedule.Year() == now.Year() &&
			todaySchedule.Month() == now.Month() &&
			todaySchedule.Day() == now.Day()

		if isToday {
			// For first-time evaluation, check if the scheduled time has arrived
			if now.Before(todaySchedule) {
				// Scheduled time is in the future, don't pause yet
				return false, nil
			}
			// For first-time evaluation, only pause if we're exactly at the scheduled time
			// or within a very small window (1 minute) to account for controller reconciliation
			if now.Equal(todaySchedule) {
				// If times are equal, we should pause
				return true, nil
			}
			// If time has passed, check if it's within a very small window (1 minute)
			// This is more restrictive for first-time evaluation to prevent immediate pausing
			if now.After(todaySchedule) {
				timeSinceSchedule := now.Sub(todaySchedule)
				if timeSinceSchedule <= 1*time.Minute {
					return true, nil
				}
				// Time has passed beyond the reasonable window, wait for next window
				return false, nil
			}
		}

		// Schedule doesn't apply to today, don't pause
		return false, nil
	}

	// Check if we should pause now
	// We need to check if the current time is after today's scheduled pause time
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	todaySchedule := cronSchedule.Next(today.Add(-time.Second)) // Get today's scheduled time

	// Check if the schedule actually applies to today (not tomorrow or later)
	isToday := todaySchedule.Year() == now.Year() &&
		todaySchedule.Month() == now.Month() &&
		todaySchedule.Day() == now.Day()

	if isToday {
		// If today's schedule is still in the future, don't pause
		if now.Before(todaySchedule) {
			return false, nil
		}
		// If we're past today's schedule, check if we haven't already paused today
		// by comparing with the reference time
		if referenceTime.After(todaySchedule) {
			// We already paused after today's schedule, don't pause again
			return false, nil
		}
		// We're past today's schedule and haven't paused yet, so pause
		return true, nil
	}

	// Schedule doesn't apply to today, don't pause
	return false, nil
}

// shouldUnpauseCluster checks if the cluster should be unpaused based on the cron schedule
func (r *PerconaServerMySQLHibernationReconciler) shouldUnpauseCluster(ctx context.Context, cr *apiv1.PerconaServerMySQL, schedule string, now time.Time) (bool, error) {
	log := logf.FromContext(ctx).WithName("shouldUnpauseCluster")

	// Parse cron schedule
	cronSchedule, err := cron.ParseStandard(schedule)
	if err != nil {
		log.Error(err, "Invalid unpause schedule", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", schedule)
		return false, errors.Wrap(err, "invalid unpause schedule")
	}

	// Check if cluster is not paused
	if !cr.Spec.Pause {
		return false, nil
	}

	// Check if hibernation is scheduled for next window - if so, don't unpause until that time
	if cr.Status.Hibernation != nil && cr.Status.Hibernation.State == apiv1.HibernationStateScheduled {
		// If we have a next unpause time scheduled, only unpause if that time has arrived
		if cr.Status.Hibernation.NextUnpauseTime != nil {
			if now.Before(cr.Status.Hibernation.NextUnpauseTime.Time) {
				log.Info("Hibernation scheduled for next window, waiting", "cluster", cr.Name, "namespace", cr.Namespace,
					"scheduledTime", cr.Status.Hibernation.NextUnpauseTime.Time, "currentTime", now)
				return false, nil
			}
			// The scheduled time has arrived, we can proceed with unpausing
			log.Info("Scheduled hibernation time has arrived, proceeding with unpause", "cluster", cr.Name, "namespace", cr.Namespace,
				"scheduledTime", cr.Status.Hibernation.NextUnpauseTime.Time, "currentTime", now)
		} else {
			// No next unpause time set, but state is scheduled - this shouldn't happen, but be safe
			return false, nil
		}
	}

	// Get reference time for calculating next unpause
	var referenceTime time.Time
	if cr.Status.Hibernation != nil && cr.Status.Hibernation.LastUnpauseTime != nil {
		// If we have a previous unpause time, use it
		referenceTime = cr.Status.Hibernation.LastUnpauseTime.Time
	} else if cr.Status.Hibernation != nil && cr.Status.Hibernation.LastPauseTime != nil {
		// If no previous unpause but we have a pause time, use that
		referenceTime = cr.Status.Hibernation.LastPauseTime.Time
	} else {
		// If no previous times, this is first-time evaluation
		// For first-time evaluation, we should NOT unpause if the scheduled time has already passed today
		// This prevents immediate unpausing when hibernation is enabled after the scheduled time
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todaySchedule := cronSchedule.Next(today.Add(-time.Second)) // Get today's scheduled time

		// Check if the schedule actually applies to today (not tomorrow or later)
		isToday := todaySchedule.Year() == now.Year() &&
			todaySchedule.Month() == now.Month() &&
			todaySchedule.Day() == now.Day()

		if isToday {
			// For first-time evaluation, we should NOT unpause regardless of whether
			// the scheduled time has passed or not - we should wait for the next window
			// This prevents immediate unpausing when hibernation is enabled
			return false, nil
		}

		// Schedule doesn't apply to today, don't unpause
		return false, nil
	}

	// Check if we should unpause now
	nextUnpauseTime := cronSchedule.Next(referenceTime)
	shouldUnpause := now.After(nextUnpauseTime) || now.Equal(nextUnpauseTime)

	// If time has passed, check if it's within a reasonable window (5 minutes)
	// This accounts for the controller's reconciliation interval
	if now.After(nextUnpauseTime) && !now.Equal(nextUnpauseTime) {
		timeSinceSchedule := now.Sub(nextUnpauseTime)
		if timeSinceSchedule <= 5*time.Minute {
			shouldUnpause = true
		} else {
			shouldUnpause = false
		}
	}

	// Additional check: if we have a reference time but current time is after today's scheduled unpause time,
	// we should still unpause (this handles the case where the cluster was paused earlier today)
	// BUT only if the reference time is NOT today's scheduled unpause time (to avoid double-unpausing)
	// This works for both LastUnpauseTime and LastPauseTime as reference
	if !shouldUnpause && referenceTime != (time.Time{}) && cr.Status.Hibernation != nil {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todaySchedule := cronSchedule.Next(today.Add(-time.Second))
		isToday := todaySchedule.Year() == now.Year() &&
			todaySchedule.Month() == now.Month() &&
			todaySchedule.Day() == now.Day()

		// Check if reference time is NOT today's scheduled time (to avoid double-unpausing)
		// We consider them the same if they're within 1 minute of each other
		timeDiff := referenceTime.Sub(todaySchedule)
		referenceIsTodaySchedule := timeDiff >= -1*time.Minute && timeDiff <= 1*time.Minute

		if isToday && !referenceIsTodaySchedule {
			if now.Equal(todaySchedule) {
				shouldUnpause = true
			} else if now.After(todaySchedule) {
				// For unpause, use a longer window (1 hour) since we want to get the cluster running
				// This is more lenient than pause operations
				timeSinceSchedule := now.Sub(todaySchedule)
				if timeSinceSchedule <= 1*time.Hour {
					shouldUnpause = true
				} else {
					shouldUnpause = false
				}
			}
		}
	}

	return shouldUnpause, nil
}

// canPauseCluster checks if the cluster can be paused (cluster is ready and no active backups/restores)
func (r *PerconaServerMySQLHibernationReconciler) canPauseCluster(ctx context.Context, cr *apiv1.PerconaServerMySQL) (bool, string, error) {
	log := logf.FromContext(ctx).WithName("canPauseCluster")

	// Check if cluster is in a ready state
	if cr.Status.State != apiv1.StateReady {
		return false, fmt.Sprintf("cluster not ready (state: %s)", cr.Status.State), nil
	}

	// Check for active backups
	backupList := &apiv1.PerconaServerMySQLBackupList{}
	if err := r.List(ctx, backupList, client.InNamespace(cr.Namespace)); err != nil {
		log.Error(err, "Failed to list backups", "cluster", cr.Name, "namespace", cr.Namespace)
		return false, "", errors.Wrap(err, "failed to list backups")
	}

	for _, backup := range backupList.Items {
		if backup.Spec.ClusterName == cr.Name {
			switch backup.Status.State {
			case apiv1.BackupStarting, apiv1.BackupRunning, apiv1.BackupNew:
				return false, fmt.Sprintf("active backup: %s (state: %s)", backup.Name, backup.Status.State), nil
			}
		}
	}

	// Check for active restores
	restoreList := &apiv1.PerconaServerMySQLRestoreList{}
	if err := r.List(ctx, restoreList, client.InNamespace(cr.Namespace)); err != nil {
		log.Error(err, "Failed to list restores", "cluster", cr.Name, "namespace", cr.Namespace)
		return false, "", errors.Wrap(err, "failed to list restores")
	}

	for _, restore := range restoreList.Items {
		if restore.Spec.ClusterName == cr.Name {
			switch restore.Status.State {
			case apiv1.RestoreStarting, apiv1.RestoreRunning, apiv1.RestoreNew:
				return false, fmt.Sprintf("active restore: %s (state: %s)", restore.Name, restore.Status.State), nil
			}
		}
	}

	return true, "", nil
}

// pauseCluster pauses the cluster by setting spec.pause to true
func (r *PerconaServerMySQLHibernationReconciler) pauseCluster(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("pauseCluster")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Set pause to true
		fresh.Spec.Pause = true

		// Update the cluster
		if err := r.Client.Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update cluster spec", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Update hibernation status
		now := metav1.Now()
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}
		fresh.Status.Hibernation.State = apiv1.HibernationStatePaused
		fresh.Status.Hibernation.LastPauseTime = &now
		fresh.Status.Hibernation.Reason = ""

		// Calculate next pause time
		if fresh.Spec.Hibernation.Schedule.Pause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Pause); err == nil {
				nextPauseTime := metav1.NewTime(cronSchedule.Next(now.Time))
				fresh.Status.Hibernation.NextPauseTime = &nextPauseTime
			} else {
				log.Error(err, "Failed to parse pause schedule for next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Pause)
			}
		}

		if err := r.Client.Status().Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update hibernation status", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		log.Info("Hibernation status updated after pause", "cluster", cr.Name, "namespace", cr.Namespace, "state", fresh.Status.Hibernation.State, "lastPauseTime", fresh.Status.Hibernation.LastPauseTime)
		return nil
	})
}

// unpauseCluster unpauses the cluster by setting spec.pause to false
func (r *PerconaServerMySQLHibernationReconciler) unpauseCluster(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("unpauseCluster")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Set pause to false
		fresh.Spec.Pause = false

		// Update the cluster
		if err := r.Client.Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update cluster spec", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Update hibernation status
		now := metav1.Now()
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}
		fresh.Status.Hibernation.State = apiv1.HibernationStateActive
		fresh.Status.Hibernation.LastUnpauseTime = &now
		fresh.Status.Hibernation.Reason = ""

		// Calculate next unpause time
		if fresh.Spec.Hibernation.Schedule.Unpause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Unpause); err == nil {
				nextUnpauseTime := metav1.NewTime(cronSchedule.Next(now.Time))
				fresh.Status.Hibernation.NextUnpauseTime = &nextUnpauseTime
			} else {
				log.Error(err, "Failed to parse unpause schedule for next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Unpause)
			}
		}

		if err := r.Client.Status().Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update hibernation status", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		log.Info("Hibernation status updated after unpause", "cluster", cr.Name, "namespace", cr.Namespace, "state", fresh.Status.Hibernation.State, "lastUnpauseTime", fresh.Status.Hibernation.LastUnpauseTime)
		return nil
	})
}

// updateHibernationState updates the hibernation status with a state and reason
func (r *PerconaServerMySQLHibernationReconciler) updateHibernationState(ctx context.Context, cr *apiv1.PerconaServerMySQL, state, reason string) error {
	log := logf.FromContext(ctx).WithName("updateHibernationState")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy for status update", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Update hibernation status
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}

		// Check if state or reason actually changed to avoid unnecessary updates and log spam
		oldState := fresh.Status.Hibernation.State
		oldReason := fresh.Status.Hibernation.Reason
		stateChanged := oldState != state
		reasonChanged := oldReason != reason

		if stateChanged || reasonChanged {
			fresh.Status.Hibernation.State = state
			fresh.Status.Hibernation.Reason = reason

			if err := r.Client.Status().Update(ctx, fresh); err != nil {
				log.Error(err, "Failed to update hibernation status", "cluster", cr.Name, "namespace", cr.Namespace, "state", state, "reason", reason)
				return err
			}

			// Only log significant state changes, not routine updates
			if stateChanged {
				log.Info("Hibernation state changed", "cluster", cr.Name, "namespace", cr.Namespace, "oldState", oldState, "newState", state, "reason", reason)
			} else if reasonChanged && reason != "" {
				log.V(1).Info("Hibernation reason updated", "cluster", cr.Name, "namespace", cr.Namespace, "state", state, "reason", reason)
			}
		}
		return nil
	})
}

// initializeHibernationStatus initializes the hibernation status with appropriate state and next times
func (r *PerconaServerMySQLHibernationReconciler) initializeHibernationStatus(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("initializeHibernationStatus")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy for status initialization", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Initialize hibernation status
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}

		// Set appropriate state based on current pause status
		if fresh.Spec.Pause {
			fresh.Status.Hibernation.State = apiv1.HibernationStatePaused
		} else {
			fresh.Status.Hibernation.State = apiv1.HibernationStateActive
		}

		now := time.Now()

		// Calculate next pause time if schedule is configured
		if fresh.Spec.Hibernation.Schedule.Pause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Pause); err == nil {
				nextPauseTime := r.calculateNextScheduleTime(now, cronSchedule)
				fresh.Status.Hibernation.NextPauseTime = &nextPauseTime
			} else {
				log.Error(err, "Failed to parse pause schedule for initial next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Pause)
			}
		}

		// Calculate next unpause time if schedule is configured
		if fresh.Spec.Hibernation.Schedule.Unpause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Unpause); err == nil {
				nextUnpauseTime := r.calculateNextScheduleTime(now, cronSchedule)
				fresh.Status.Hibernation.NextUnpauseTime = &nextUnpauseTime
			} else {
				log.Error(err, "Failed to parse unpause schedule for initial next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Unpause)
			}
		}

		// Don't set lastPauseTime or lastUnpauseTime here - they should only be set when actual pause/unpause occurs

		if err := r.Client.Status().Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to initialize hibernation status", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		return nil
	})
}

// updateHibernationScheduleIfChanged checks if the hibernation schedule has changed and updates next times if needed
func (r *PerconaServerMySQLHibernationReconciler) updateHibernationScheduleIfChanged(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("updateHibernationScheduleIfChanged")

	// Get the current hibernation status
	if cr.Status.Hibernation == nil {
		return nil // Nothing to update
	}

	// Check if we need to initialize missing times or if schedule strings have changed
	needsUpdate := false

	// Check if pause schedule has changed or is missing
	if cr.Spec.Hibernation.Schedule.Pause != "" {
		if cr.Status.Hibernation.NextPauseTime == nil {
			needsUpdate = true
			log.Info("Initializing missing next pause time", "cluster", cr.Name, "namespace", cr.Namespace)
		} else {
			// Check if the schedule string has changed by comparing with current calculated time
			if cronSchedule, err := cron.ParseStandard(cr.Spec.Hibernation.Schedule.Pause); err == nil {
				expectedNextPauseTime := r.calculateNextScheduleTime(time.Now(), cronSchedule)
				currentNextPauseTime := cr.Status.Hibernation.NextPauseTime

				// Only update if the calculated time is significantly different (more than 1 hour)
				// This prevents race conditions while still detecting real schedule changes
				timeDiff := expectedNextPauseTime.Sub(currentNextPauseTime.Time)
				if timeDiff > time.Hour || timeDiff < -time.Hour {
					needsUpdate = true
					log.Info("Pause schedule changed, updating next pause time", "cluster", cr.Name, "namespace", cr.Namespace,
						"oldTime", currentNextPauseTime, "newTime", expectedNextPauseTime)
				} else {
					// Check if the new schedule time is very close in the future (within 5 minutes)
					// This handles the case where user changes schedule to a time very close to now
					now := time.Now()
					if expectedNextPauseTime.Time.After(now) && expectedNextPauseTime.Time.Sub(now) <= 5*time.Minute {
						needsUpdate = true
						log.Info("Schedule changed to very near future time, updating to pause soon", "cluster", cr.Name, "namespace", cr.Namespace,
							"oldTime", currentNextPauseTime, "newTime", expectedNextPauseTime, "timeUntilPause", expectedNextPauseTime.Time.Sub(now))
					}
				}
			}
		}
	}

	// Check if unpause schedule has changed or is missing
	if cr.Spec.Hibernation.Schedule.Unpause != "" {
		if cr.Status.Hibernation.NextUnpauseTime == nil {
			needsUpdate = true
			log.Info("Initializing missing next unpause time", "cluster", cr.Name, "namespace", cr.Namespace)
		} else {
			// Check if the schedule string has changed by comparing with current calculated time
			if cronSchedule, err := cron.ParseStandard(cr.Spec.Hibernation.Schedule.Unpause); err == nil {
				expectedNextUnpauseTime := r.calculateNextScheduleTime(time.Now(), cronSchedule)
				currentNextUnpauseTime := cr.Status.Hibernation.NextUnpauseTime

				// Only update if the calculated time is significantly different (more than 1 hour)
				// This prevents race conditions while still detecting real schedule changes
				timeDiff := expectedNextUnpauseTime.Sub(currentNextUnpauseTime.Time)
				if timeDiff > time.Hour || timeDiff < -time.Hour {
					needsUpdate = true
					log.Info("Unpause schedule changed, updating next unpause time", "cluster", cr.Name, "namespace", cr.Namespace,
						"oldTime", currentNextUnpauseTime, "newTime", expectedNextUnpauseTime)
				} else {
					// Check if the new schedule time is very close in the future (within 5 minutes)
					// This handles the case where user changes schedule to a time very close to now
					now := time.Now()
					if expectedNextUnpauseTime.Time.After(now) && expectedNextUnpauseTime.Time.Sub(now) <= 5*time.Minute {
						needsUpdate = true
						log.Info("Unpause schedule changed to very near future time, updating to unpause soon", "cluster", cr.Name, "namespace", cr.Namespace,
							"oldTime", currentNextUnpauseTime, "newTime", expectedNextUnpauseTime, "timeUntilUnpause", expectedNextUnpauseTime.Time.Sub(now))
					}
				}
			}
		}
	}

	// Update the status if needed
	if needsUpdate {
		return r.updateHibernationNextTimes(ctx, cr)
	}

	return nil
}

// updateHibernationNextTimes updates the next pause and unpause times in the hibernation status
func (r *PerconaServerMySQLHibernationReconciler) updateHibernationNextTimes(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("updateHibernationNextTimes")

	return k8sretry.RetryOnConflict(k8sretry.DefaultRetry, func() error {
		// Get fresh copy of the cluster
		fresh := &apiv1.PerconaServerMySQL{}
		if err := r.Client.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, fresh); err != nil {
			log.Error(err, "Failed to get fresh cluster copy for schedule update", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		// Ensure hibernation status exists
		if fresh.Status.Hibernation == nil {
			fresh.Status.Hibernation = &apiv1.HibernationStatus{}
		}

		now := time.Now()

		// Update next pause time
		if fresh.Spec.Hibernation.Schedule.Pause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Pause); err == nil {
				nextPauseTime := r.calculateNextScheduleTime(now, cronSchedule)
				fresh.Status.Hibernation.NextPauseTime = &nextPauseTime
			} else {
				log.Error(err, "Failed to parse pause schedule for next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Pause)
			}
		}

		// Update next unpause time
		if fresh.Spec.Hibernation.Schedule.Unpause != "" {
			if cronSchedule, err := cron.ParseStandard(fresh.Spec.Hibernation.Schedule.Unpause); err == nil {
				nextUnpauseTime := r.calculateNextScheduleTime(now, cronSchedule)
				fresh.Status.Hibernation.NextUnpauseTime = &nextUnpauseTime
			} else {
				log.Error(err, "Failed to parse unpause schedule for next time calculation", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", fresh.Spec.Hibernation.Schedule.Unpause)
			}
		}

		// Update the status
		if err := r.Client.Status().Update(ctx, fresh); err != nil {
			log.Error(err, "Failed to update hibernation next times", "cluster", cr.Name, "namespace", cr.Namespace)
			return err
		}

		log.Info("Hibernation next times updated", "cluster", cr.Name, "namespace", cr.Namespace,
			"nextPauseTime", fresh.Status.Hibernation.NextPauseTime,
			"nextUnpauseTime", fresh.Status.Hibernation.NextUnpauseTime)

		return nil
	})
}

// checkAndScheduleForNextWindow checks if the scheduled time has passed and schedules for next window if needed
func (r *PerconaServerMySQLHibernationReconciler) checkAndScheduleForNextWindow(ctx context.Context, cr *apiv1.PerconaServerMySQL, schedule string, now time.Time) error {
	log := logf.FromContext(ctx).WithName("checkAndScheduleForNextWindow")

	// Parse cron schedule
	cronSchedule, err := cron.ParseStandard(schedule)
	if err != nil {
		return errors.Wrap(err, "invalid schedule")
	}

	// Check if this is a first-time evaluation (no previous pause/unpause times)
	if cr.Status.Hibernation == nil || (cr.Status.Hibernation.LastPauseTime == nil && cr.Status.Hibernation.LastUnpauseTime == nil) {
		// Calculate today's scheduled time
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		todaySchedule := cronSchedule.Next(today.Add(-time.Second))

		// Check if the schedule applies to today
		isToday := todaySchedule.Year() == now.Year() &&
			todaySchedule.Month() == now.Month() &&
			todaySchedule.Day() == now.Day()

		if isToday {
			// Check if the scheduled time has passed beyond the 5-minute window
			if now.After(todaySchedule) {
				timeSinceSchedule := now.Sub(todaySchedule)
				if timeSinceSchedule > 5*time.Minute {
					// The scheduled time has passed beyond the reasonable window
					// Schedule for the next window
					reason := fmt.Sprintf("Scheduled time passed beyond 5-minute window (passed %v ago)", timeSinceSchedule)
					log.Info("Scheduled time has passed, scheduling for next window", "cluster", cr.Name, "namespace", cr.Namespace,
						"schedule", schedule, "todaySchedule", todaySchedule, "timeSince", timeSinceSchedule)

					return r.scheduleHibernationForNextWindow(ctx, cr, schedule, reason)
				}
			}
		}
	}

	return nil
}

// calculateNextScheduleTime calculates the next schedule time, considering if today's time is still available
func (r *PerconaServerMySQLHibernationReconciler) calculateNextScheduleTime(now time.Time, cronSchedule cron.Schedule) metav1.Time {
	// Use UTC for all calculations to ensure consistency
	utcNow := now.UTC()

	// Get today's start time in UTC
	today := time.Date(utcNow.Year(), utcNow.Month(), utcNow.Day(), 0, 0, 0, 0, time.UTC)

	// Calculate today's scheduled time
	todaySchedule := cronSchedule.Next(today.Add(-time.Second))

	// If today's scheduled time is still in the future, use it
	if todaySchedule.After(utcNow) {
		return metav1.NewTime(todaySchedule)
	}

	// If today's scheduled time has passed, return the next occurrence (next window)
	nextSchedule := cronSchedule.Next(utcNow)
	return metav1.NewTime(nextSchedule)
}

// proactivelyScheduleForNextWindow proactively schedules hibernation for the next window when cluster is in unready state
// This prevents immediate pausing when the cluster becomes ready
func (r *PerconaServerMySQLHibernationReconciler) proactivelyScheduleForNextWindow(ctx context.Context, cr *apiv1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("proactivelyScheduleForNextWindow")

	// Only check if hibernation is enabled and has a pause schedule
	if !cr.IsHibernationEnabled() || cr.Spec.Hibernation == nil || cr.Spec.Hibernation.Schedule.Pause == "" {
		return nil
	}

	// Parse cron schedule
	cronSchedule, err := cron.ParseStandard(cr.Spec.Hibernation.Schedule.Pause)
	if err != nil {
		log.Error(err, "Invalid pause schedule during proactive scheduling", "cluster", cr.Name, "namespace", cr.Namespace, "schedule", cr.Spec.Hibernation.Schedule.Pause)
		return err
	}

	now := time.Now().UTC()

	// Calculate today's scheduled time
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	todaySchedule := cronSchedule.Next(today.Add(-time.Second))

	// Check if the schedule applies to today
	isToday := todaySchedule.Year() == now.Year() &&
		todaySchedule.Month() == now.Month() &&
		todaySchedule.Day() == now.Day()

	if !isToday {
		return nil // Schedule doesn't apply to today
	}

	// Check if we have a nextPauseTime set and if it's still today's time
	if cr.Status.Hibernation != nil && cr.Status.Hibernation.NextPauseTime != nil {
		currentNextPauseTime := cr.Status.Hibernation.NextPauseTime.Time

		// If the current nextPauseTime is still today's scheduled time, we need to update it to next window
		if currentNextPauseTime.Year() == now.Year() &&
			currentNextPauseTime.Month() == now.Month() &&
			currentNextPauseTime.Day() == now.Day() &&
			currentNextPauseTime.Hour() == todaySchedule.Hour() &&
			currentNextPauseTime.Minute() == todaySchedule.Minute() {

			// Check if the scheduled time has passed
			if now.After(todaySchedule) {
				// The nextPauseTime is still set to today's schedule, but the time has passed
				// Proactively update it to next window
				reason := fmt.Sprintf("Scheduled time passed while cluster was unready, proactively scheduling for next window (state: %s)", cr.Status.State)
				log.Info("Scheduled time passed while cluster was unready, proactively scheduling for next window", "cluster", cr.Name, "namespace", cr.Namespace,
					"schedule", cr.Spec.Hibernation.Schedule.Pause, "todaySchedule", todaySchedule, "currentNextPauseTime", currentNextPauseTime, "clusterState", cr.Status.State, "currentTime", now)

				return r.scheduleHibernationForNextWindow(ctx, cr, cr.Spec.Hibernation.Schedule.Pause, reason)
			}
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PerconaServerMySQLHibernationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.PerconaServerMySQL{}).
		Named("pshibernation-controller").
		Complete(r)
}
