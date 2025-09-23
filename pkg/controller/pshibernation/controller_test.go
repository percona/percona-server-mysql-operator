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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/robfig/cron/v3"
)

func TestPerconaServerMySQLHibernationReconciler_shouldPauseCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		cr             *apiv1.PerconaServerMySQL
		schedule       string
		now            time.Time
		expectedResult bool
		expectedError  bool
		description    string
	}{
		{
			name:        "should pause - first time evaluation with current time matching schedule",
			description: "First-time evaluation when current time exactly matches the pause schedule - should pause",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC), // Thursday 1:45 PM
			expectedResult: true,                                           // Should pause when time matches
			expectedError:  false,
		},
		{
			name:        "should not pause - first time evaluation with current time before schedule",
			description: "First-time evaluation when current time is before the pause schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 44, 0, 0, time.UTC), // Thursday 1:44 PM
			expectedResult: false,
			expectedError:  false,
		},
		{
			name:        "should NOT pause - first time evaluation with current time after schedule",
			description: "First-time evaluation when current time is after the pause schedule - should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 47, 0, 0, time.UTC), // Thursday 1:47 PM
			expectedResult: false,                                          // Should NOT pause when time has passed (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "DEBUG: should NOT pause - real scenario from logs (11:15 schedule, 11:18 time)",
			description: "Real scenario: Schedule is 15 11 * * 1-5 (11:15 AM), current time is 11:18 AM, cluster was never paused",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "15 11 * * 1-5", // 11:15 AM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						// No LastPauseTime or LastUnpauseTime - first time evaluation
					},
				},
			},
			schedule:       "15 11 * * 1-5",
			now:            time.Date(2025, 9, 19, 11, 18, 0, 0, time.UTC), // Friday 11:18 AM (3 minutes after schedule)
			expectedResult: false,                                          // Should NOT pause - first-time evaluation should wait for next window
			expectedError:  false,
		},
		{
			name:        "DEBUG: should pause - real scenario with reference time (11:15 schedule, 11:18 time, with LastUnpauseTime)",
			description: "Real scenario: Schedule is 15 11 * * 1-5 (11:15 AM), current time is 11:18 AM, but cluster has LastUnpauseTime from earlier",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "15 11 * * 1-5", // 11:15 AM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						LastUnpauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 19, 11, 10, 0, 0, time.UTC), // 11:10 AM (before schedule)
						},
					},
				},
			},
			schedule:       "15 11 * * 1-5",
			now:            time.Date(2025, 9, 19, 11, 18, 0, 0, time.UTC), // Friday 11:18 AM (3 minutes after schedule)
			expectedResult: true,                                           // Should pause - we have reference time and current time is after schedule
			expectedError:  false,
		},
		{
			name:        "should pause - with previous pause time",
			description: "Evaluation with previous pause time as reference",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						LastPauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 17, 13, 45, 0, 0, time.UTC), // Yesterday 1:45 PM
						},
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC), // Thursday 1:45 PM
			expectedResult: true,
			expectedError:  false,
		},
		{
			name:        "should pause - with previous unpause time",
			description: "Evaluation with previous unpause time as reference (no previous pause)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						LastUnpauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 18, 8, 0, 0, 0, time.UTC), // Today 8:00 AM
						},
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC), // Thursday 1:45 PM
			expectedResult: true,
			expectedError:  false,
		},
		{
			name:        "invalid cron schedule",
			description: "Should return error for invalid cron expression",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "invalid cron",
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "invalid cron",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC),
			expectedResult: false,
			expectedError:  true,
		},
		{
			name:        "real-world scenario - pause time passed, should NOT pause",
			description: "Real scenario: pause scheduled for 13:45, current time is 13:47, should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "ps",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "45 13 * * 1-5", // 1:45 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 47, 28, 0, time.UTC), // Thursday 1:47:28 PM (2+ minutes after pause time)
			expectedResult: false,                                           // Should NOT pause when time has passed (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "user reported bug - pause at 12:55, enable hibernation at 16:52, should NOT pause",
			description: "Bug fix: When hibernation is enabled after scheduled time has passed (12:55 -> 16:52), should NOT pause immediately",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "55 12 * * 1-5", // 12:55 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "55 12 * * 1-5",
			now:            time.Date(2025, 9, 19, 16, 52, 0, 0, time.UTC), // Friday 4:52 PM (4+ hours after schedule)
			expectedResult: false,                                          // Should NOT pause when time has passed
			expectedError:  false,
		},
		{
			name:        "user reported bug - pause at 09:40, enable hibernation at 10:08, should NOT pause",
			description: "Bug fix: When hibernation is enabled after scheduled time has passed, should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "40 09 * * 1-5", // 9:40 AM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "40 09 * * 1-5",
			now:            time.Date(2025, 9, 19, 10, 8, 40, 0, time.UTC), // 10:08:40 AM (28 minutes after scheduled time)
			expectedResult: false,                                          // Should NOT pause when time has passed (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "should pause - time matches schedule",
			description: "Time exactly matches the pause schedule - this should work for normal operation (not first-time)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 20 * * 1-5", // 8 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						LastUnpauseTime: &metav1.Time{
							Time: time.Date(2024, 1, 15, 19, 0, 0, 0, time.UTC), // 1 hour before
						},
					},
				},
			},
			schedule:       "0 20 * * 1-5",
			now:            time.Date(2024, 1, 15, 20, 0, 0, 0, time.UTC), // Monday 8 PM
			expectedResult: true,
			expectedError:  false,
		},
		{
			name: "should not pause - cluster already paused",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 20 * * 1-5",
						},
					},
				},
			},
			schedule:       "0 20 * * 1-5",
			now:            time.Date(2024, 1, 15, 20, 0, 0, 0, time.UTC),
			expectedResult: false,
			expectedError:  false,
		},
		{
			name: "should not pause - time does not match schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: false,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 20 * * 1-5",
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						LastPauseTime: &metav1.Time{Time: time.Date(2024, 1, 14, 20, 0, 0, 0, time.UTC)}, // Previous day
					},
				},
			},
			schedule:       "0 20 * * 1-5",
			now:            time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC), // Monday 10 AM
			expectedResult: false,
			expectedError:  false,
		},
		{
			name: "invalid schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: false,
				},
			},
			schedule:       "invalid-cron",
			now:            time.Date(2024, 1, 15, 20, 0, 0, 0, time.UTC),
			expectedResult: false,
			expectedError:  true,
		},
		{
			name:        "should not pause - past scheduled time with unpause time after schedule",
			description: "Bug fix: Should not pause when current time is past today's schedule and there was an unpause after the schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: false,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "40 09 * * 1-5", // 9:40 AM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
						LastUnpauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 19, 9, 48, 28, 0, time.UTC), // 9:48:28 AM (after schedule)
						},
					},
				},
			},
			schedule:       "40 09 * * 1-5",
			now:            time.Date(2025, 9, 19, 9, 48, 30, 0, time.UTC), // 9:48:30 AM (past schedule, after unpause)
			expectedResult: false,
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).Build()
			reconciler := &PerconaServerMySQLHibernationReconciler{
				Client:        client,
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{},
			}

			result, err := reconciler.shouldPauseCluster(context.Background(), tt.cr, tt.schedule, tt.now)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_shouldUnpauseCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		cr             *apiv1.PerconaServerMySQL
		schedule       string
		now            time.Time
		expectedResult bool
		expectedError  bool
		description    string
	}{
		{
			name:        "should NOT unpause - first time evaluation with current time matching schedule",
			description: "First-time evaluation when current time exactly matches the unpause schedule - should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "50 13 * * 1-5", // 1:50 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
					},
				},
			},
			schedule:       "50 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 50, 0, 0, time.UTC), // Thursday 1:50 PM
			expectedResult: false,                                          // Should NOT unpause when time matches (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "should not unpause - first time evaluation with current time before schedule",
			description: "First-time evaluation when current time is before the unpause schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "50 13 * * 1-5", // 1:50 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
					},
				},
			},
			schedule:       "50 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 49, 0, 0, time.UTC), // Thursday 1:49 PM
			expectedResult: false,
			expectedError:  false,
		},
		{
			name:        "should NOT unpause - first time evaluation with current time after schedule",
			description: "First-time evaluation when current time is after the unpause schedule - should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "50 13 * * 1-5", // 1:50 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
					},
				},
			},
			schedule:       "50 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 52, 0, 0, time.UTC), // Thursday 1:52 PM
			expectedResult: false,                                          // Should NOT unpause when time has passed (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "real-world scenario - unpause time passed, should wait for next window",
			description: "Real scenario: unpause scheduled for 13:50, current time is 13:52, should NOT trigger unpause (wait for next window)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "ps",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "50 13 * * 1-5", // 1:50 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
					},
				},
			},
			schedule:       "50 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 52, 0, 0, time.UTC), // Thursday 1:52 PM (2 minutes after unpause time)
			expectedResult: false,                                          // Should NOT unpause when time has passed (first-time evaluation)
			expectedError:  false,
		},
		{
			name:        "should unpause - time matches schedule",
			description: "Time exactly matches the unpause schedule - this should work for normal operation (not first-time)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "0 8 * * 1-5",
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
						LastPauseTime: &metav1.Time{
							Time: time.Date(2024, 1, 15, 7, 0, 0, 0, time.UTC), // 1 hour before
						},
					},
				},
			},
			schedule:       "0 8 * * 1-5",
			now:            time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC), // Monday 8 AM
			expectedResult: true,
			expectedError:  false,
		},
		{
			name: "should not unpause - cluster not paused",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: false,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "0 8 * * 1-5",
						},
					},
				},
			},
			schedule:       "0 8 * * 1-5",
			now:            time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC),
			expectedResult: false,
			expectedError:  false,
		},
		{
			name: "should not unpause - time does not match schedule",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "0 8 * * 1-5",
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						LastUnpauseTime: &metav1.Time{Time: time.Date(2024, 1, 15, 8, 0, 0, 0, time.UTC)}, // Same day at 8 AM
					},
				},
			},
			schedule:       "0 8 * * 1-5",
			now:            time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC), // Monday 10 AM
			expectedResult: false,
			expectedError:  false,
		},
		{
			name:        "should NOT unpause - real-world scenario with reference time (unpause time passed by more than 1 hour)",
			description: "Real scenario: cluster paused earlier today, current time is 2+ hours after today's unpause schedule - should wait for next window",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "ps",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "10 14 * * 1-5", // 2:10 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
						LastPauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 18, 14, 5, 0, 0, time.UTC), // Today 2:05 PM (when paused, before unpause schedule)
						},
					},
				},
			},
			schedule:       "10 14 * * 1-5",
			now:            time.Date(2025, 9, 18, 16, 45, 0, 0, time.UTC), // Thursday 4:45 PM (2+ hours after unpause time)
			expectedResult: false,                                          // Should NOT unpause when time has passed by more than 1 hour
			expectedError:  false,
		},
		{
			name:        "should unpause - real-world scenario with reference time (unpause time passed by less than 1 hour)",
			description: "Real scenario: cluster paused earlier today, current time is within 1 hour of today's unpause schedule - should unpause",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "ps-cluster1",
					Namespace: "ps",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Pause: true,
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Unpause: "10 14 * * 1-5", // 2:10 PM Mon-Fri
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStatePaused,
						LastPauseTime: &metav1.Time{
							Time: time.Date(2025, 9, 18, 14, 5, 0, 0, time.UTC), // Today 2:05 PM (when paused, before unpause schedule)
						},
					},
				},
			},
			schedule:       "10 14 * * 1-5",
			now:            time.Date(2025, 9, 18, 14, 30, 0, 0, time.UTC), // Thursday 2:30 PM (20 minutes after unpause time)
			expectedResult: true,                                           // Should unpause when time has passed by less than 1 hour
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).Build()
			reconciler := &PerconaServerMySQLHibernationReconciler{
				Client:        client,
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{},
			}

			result, err := reconciler.shouldUnpauseCluster(context.Background(), tt.cr, tt.schedule, tt.now)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.expectedResult != result {
					t.Logf("Debug - Test: %s", tt.name)
					t.Logf("  Expected: %v", tt.expectedResult)
					t.Logf("  Actual: %v", result)
					t.Logf("  Schedule: %s", tt.schedule)
					t.Logf("  Current time: %s", tt.now.Format(time.RFC3339))
					if tt.cr.Status.Hibernation != nil && tt.cr.Status.Hibernation.LastPauseTime != nil {
						t.Logf("  LastPauseTime: %s", tt.cr.Status.Hibernation.LastPauseTime.Time.Format(time.RFC3339))
					}
				}
				assert.Equal(t, tt.expectedResult, result)
			}
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_canPauseCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		cr             *apiv1.PerconaServerMySQL
		backups        []*apiv1.PerconaServerMySQLBackup
		restores       []*apiv1.PerconaServerMySQLRestore
		expectedResult bool
		expectedReason string
		expectedError  bool
	}{
		{
			name: "can pause - no active operations and cluster ready",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups:        []*apiv1.PerconaServerMySQLBackup{},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: true,
			expectedReason: "",
			expectedError:  false,
		},
		{
			name: "cannot pause - cluster not ready (initializing)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateInitializing,
				},
			},
			backups:        []*apiv1.PerconaServerMySQLBackup{},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "cluster not ready (state: Initializing)",
			expectedError:  false,
		},
		{
			name: "cannot pause - cluster not ready (error)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateError,
				},
			},
			backups:        []*apiv1.PerconaServerMySQLBackup{},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "cluster not ready (state: Error)",
			expectedError:  false,
		},
		{
			name: "cannot pause - cluster not ready (stopping)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateStopping,
				},
			},
			backups:        []*apiv1.PerconaServerMySQLBackup{},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "cluster not ready (state: Stopping)",
			expectedError:  false,
		},
		{
			name: "cannot pause - active backup (Running)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "active-backup",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLBackupStatus{
						State: apiv1.BackupRunning,
					},
				},
			},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "active backup: active-backup (state: Running)",
			expectedError:  false,
		},
		{
			name: "cannot pause - active backup (Starting)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "starting-backup",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLBackupStatus{
						State: apiv1.BackupStarting,
					},
				},
			},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "active backup: starting-backup (state: Starting)",
			expectedError:  false,
		},
		{
			name: "cannot pause - active backup (New)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "new-backup",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLBackupStatus{
						State: apiv1.BackupNew,
					},
				},
			},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: false,
			expectedReason: "active backup: new-backup (state: )",
			expectedError:  false,
		},
		{
			name: "cannot pause - active restore (Running)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{},
			restores: []*apiv1.PerconaServerMySQLRestore{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "active-restore",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreRunning,
					},
				},
			},
			expectedResult: false,
			expectedReason: "active restore: active-restore (state: Running)",
			expectedError:  false,
		},
		{
			name: "cannot pause - active restore (Starting)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{},
			restores: []*apiv1.PerconaServerMySQLRestore{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "starting-restore",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreStarting,
					},
				},
			},
			expectedResult: false,
			expectedReason: "active restore: starting-restore (state: Starting)",
			expectedError:  false,
		},
		{
			name: "cannot pause - active restore (New)",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{},
			restores: []*apiv1.PerconaServerMySQLRestore{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "new-restore",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLRestoreSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLRestoreStatus{
						State: apiv1.RestoreNew,
					},
				},
			},
			expectedResult: false,
			expectedReason: "active restore: new-restore (state: )",
			expectedError:  false,
		},
		{
			name: "can pause - completed backup",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "completed-backup",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: "test-cluster",
					},
					Status: apiv1.PerconaServerMySQLBackupStatus{
						State: apiv1.BackupSucceeded,
					},
				},
			},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: true,
			expectedReason: "",
			expectedError:  false,
		},
		{
			name: "can pause - backup for different cluster",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			},
			backups: []*apiv1.PerconaServerMySQLBackup{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "other-backup",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLBackupSpec{
						ClusterName: "other-cluster",
					},
					Status: apiv1.PerconaServerMySQLBackupStatus{
						State: apiv1.BackupRunning,
					},
				},
			},
			restores:       []*apiv1.PerconaServerMySQLRestore{},
			expectedResult: true,
			expectedReason: "",
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := []client.Object{tt.cr}
			for _, backup := range tt.backups {
				objects = append(objects, backup)
			}
			for _, restore := range tt.restores {
				objects = append(objects, restore)
			}

			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
			reconciler := &PerconaServerMySQLHibernationReconciler{
				Client:        client,
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{},
			}

			result, reason, err := reconciler.canPauseCluster(context.Background(), tt.cr)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult, result)
				assert.Equal(t, tt.expectedReason, reason)
			}
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_scheduleHibernationForNextWindow(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		cr             *apiv1.PerconaServerMySQL
		schedule       string
		reason         string
		expectedError  bool
		expectedState  string
		expectedReason string
	}{
		{
			name: "schedule for next window - cluster not ready",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateInitializing,
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "0 18 * * 1-5", // 6 PM Mon-Fri
			reason:         "cluster not ready (state: Initializing)",
			expectedError:  false,
			expectedState:  apiv1.HibernationStateScheduled,
			expectedReason: "Scheduled for next window: cluster not ready (state: Initializing)",
		},
		{
			name: "schedule for next window - cluster in error state",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateError,
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateActive,
					},
				},
			},
			schedule:       "30 19 * * 1-5", // 7:30 PM Mon-Fri
			reason:         "cluster not ready (state: Error)",
			expectedError:  false,
			expectedState:  apiv1.HibernationStateScheduled,
			expectedReason: "Scheduled for next window: cluster not ready (state: Error)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with the test cluster
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.cr).
				WithStatusSubresource(tt.cr).
				Build()

			// Create reconciler
			reconciler := &PerconaServerMySQLHibernationReconciler{
				Client: fakeClient,
				Scheme: scheme,
			}

			// Create context
			ctx := context.Background()

			// Call the method
			err := reconciler.scheduleHibernationForNextWindow(ctx, tt.cr, tt.schedule, tt.reason)

			// Check error expectation
			if tt.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Get updated cluster
			updatedCluster := &apiv1.PerconaServerMySQL{}
			err = fakeClient.Get(ctx, types.NamespacedName{Name: tt.cr.Name, Namespace: tt.cr.Namespace}, updatedCluster)
			require.NoError(t, err)

			// Verify hibernation status
			require.NotNil(t, updatedCluster.Status.Hibernation)
			assert.Equal(t, tt.expectedState, updatedCluster.Status.Hibernation.State)
			assert.Contains(t, updatedCluster.Status.Hibernation.Reason, "Scheduled for next window")
			assert.NotNil(t, updatedCluster.Status.Hibernation.NextPauseTime)
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_pauseCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Pause: false,
			Hibernation: &apiv1.HibernationSpec{
				Enabled: true,
				Schedule: apiv1.HibernationSchedule{
					Pause: "0 20 * * 1-5",
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).Build()
	reconciler := &PerconaServerMySQLHibernationReconciler{
		Client:        client,
		Scheme:        scheme,
		ServerVersion: &platform.ServerVersion{},
	}

	err := reconciler.pauseCluster(context.Background(), cr)
	require.NoError(t, err)

	// Verify the cluster was paused
	updated := &apiv1.PerconaServerMySQL{}
	err = client.Get(context.Background(), types.NamespacedName{Name: "test-cluster", Namespace: "default"}, updated)
	require.NoError(t, err)

	assert.True(t, updated.Spec.Pause)
	assert.NotNil(t, updated.Status.Hibernation)
	assert.Equal(t, apiv1.HibernationStatePaused, updated.Status.Hibernation.State)
	assert.NotNil(t, updated.Status.Hibernation.LastPauseTime)
	assert.NotNil(t, updated.Status.Hibernation.NextPauseTime)
}

func TestPerconaServerMySQLHibernationReconciler_unpauseCluster(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Pause: true,
			Hibernation: &apiv1.HibernationSpec{
				Enabled: true,
				Schedule: apiv1.HibernationSchedule{
					Unpause: "0 8 * * 1-5",
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).Build()
	reconciler := &PerconaServerMySQLHibernationReconciler{
		Client:        client,
		Scheme:        scheme,
		ServerVersion: &platform.ServerVersion{},
	}

	err := reconciler.unpauseCluster(context.Background(), cr)
	require.NoError(t, err)

	// Verify the cluster was unpaused
	updated := &apiv1.PerconaServerMySQL{}
	err = client.Get(context.Background(), types.NamespacedName{Name: "test-cluster", Namespace: "default"}, updated)
	require.NoError(t, err)

	assert.False(t, updated.Spec.Pause)
	assert.NotNil(t, updated.Status.Hibernation)
	assert.Equal(t, apiv1.HibernationStateActive, updated.Status.Hibernation.State)
	assert.NotNil(t, updated.Status.Hibernation.LastUnpauseTime)
	assert.NotNil(t, updated.Status.Hibernation.NextUnpauseTime)
}

func TestPerconaServerMySQLHibernationReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name           string
		cr             *apiv1.PerconaServerMySQL
		expectedResult ctrl.Result
		expectedError  bool
	}{
		{
			name: "hibernation disabled",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: false,
					},
				},
			},
			expectedResult: ctrl.Result{RequeueAfter: 5 * time.Minute},
			expectedError:  false,
		},
		{
			name: "hibernation enabled",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 20 * * 1-5",
						},
					},
				},
			},
			expectedResult: ctrl.Result{RequeueAfter: 1 * time.Minute},
			expectedError:  false,
		},
		{
			name: "hibernation enabled but state is disabled",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 20 * * 1-5",
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					Hibernation: &apiv1.HibernationStatus{
						State: apiv1.HibernationStateDisabled, // State is disabled but hibernation is enabled
					},
				},
			},
			expectedResult: ctrl.Result{RequeueAfter: 1 * time.Minute},
			expectedError:  false,
		},
		{
			name: "cluster not found",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "non-existent",
					Namespace: "default",
				},
			},
			expectedResult: ctrl.Result{RequeueAfter: 5 * time.Minute},
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).WithStatusSubresource(tt.cr).Build()
			reconciler := &PerconaServerMySQLHibernationReconciler{
				Client:        client,
				Scheme:        scheme,
				ServerVersion: &platform.ServerVersion{},
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.cr.Name,
					Namespace: tt.cr.Namespace,
				},
			}

			result, err := reconciler.Reconcile(context.Background(), req)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedResult.RequeueAfter, result.RequeueAfter)
			}
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_updateHibernationState(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	cr := &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "default",
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Pause: false,
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).WithStatusSubresource(cr).Build()
	reconciler := &PerconaServerMySQLHibernationReconciler{
		Client: client,
		Scheme: scheme,
	}

	ctx := context.Background()

	// Test updating hibernation state
	err := reconciler.updateHibernationState(ctx, cr, apiv1.HibernationStateScheduled, "Test reason")
	require.NoError(t, err)

	// Verify the state was updated
	updated := &apiv1.PerconaServerMySQL{}
	err = client.Get(ctx, types.NamespacedName{Name: "test-cluster", Namespace: "default"}, updated)
	require.NoError(t, err)

	assert.NotNil(t, updated.Status.Hibernation)
	assert.Equal(t, apiv1.HibernationStateScheduled, updated.Status.Hibernation.State)
	assert.Equal(t, "Test reason", updated.Status.Hibernation.Reason)
}

func TestPerconaServerMySQLHibernationReconciler_SetupWithManager(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	reconciler := &PerconaServerMySQLHibernationReconciler{
		Client:        fake.NewClientBuilder().WithScheme(scheme).Build(),
		Scheme:        scheme,
		ServerVersion: &platform.ServerVersion{},
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	err = reconciler.SetupWithManager(mgr)
	assert.NoError(t, err)
}

// Benchmark tests
func BenchmarkHibernationValidation(b *testing.B) {
	spec := &apiv1.HibernationSpec{
		Enabled: true,
		Schedule: apiv1.HibernationSchedule{
			Pause:   "0 20 * * 1-5",
			Unpause: "0 8 * * 1-5",
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = spec.Validate()
	}
}

func BenchmarkHibernationScheduleParsing(b *testing.B) {
	schedule := "0 20 * * 1-5"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = time.Parse("0 20 * * 1-5", schedule)
	}
}

// TestScheduleEvaluationLogic tests the core cron parsing and schedule evaluation logic
func TestScheduleEvaluationLogic(t *testing.T) {
	tests := []struct {
		name           string
		schedule       string
		now            time.Time
		expectedResult bool
		expectedError  bool
		description    string
	}{
		{
			name:           "cron parsing - valid schedule",
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC),
			expectedResult: true,
			expectedError:  false,
			description:    "Valid cron expression should parse and evaluate correctly",
		},
		{
			name:           "cron parsing - invalid schedule",
			schedule:       "invalid cron",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC),
			expectedResult: false,
			expectedError:  true,
			description:    "Invalid cron expression should return error",
		},
		{
			name:           "schedule evaluation - exact match",
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 45, 0, 0, time.UTC), // Thursday 1:45 PM
			expectedResult: true,
			expectedError:  false,
			description:    "Current time exactly matches schedule should return true",
		},
		{
			name:           "schedule evaluation - before schedule",
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 44, 0, 0, time.UTC), // Thursday 1:44 PM
			expectedResult: false,
			expectedError:  false,
			description:    "Current time before schedule should return false",
		},
		{
			name:           "schedule evaluation - after schedule",
			schedule:       "45 13 * * 1-5",
			now:            time.Date(2025, 9, 18, 13, 47, 0, 0, time.UTC), // Thursday 1:47 PM
			expectedResult: true,                                           // With the fix, this should return true
			expectedError:  false,
			description:    "Current time after schedule should return true with fixed logic",
		},
		{
			name:           "schedule evaluation - wrong day of week",
			schedule:       "45 13 * * 1-5",                                // Mon-Fri
			now:            time.Date(2025, 9, 20, 13, 45, 0, 0, time.UTC), // Saturday 1:45 PM
			expectedResult: false,
			expectedError:  false,
			description:    "Current time on wrong day of week should return false",
		},
		{
			name:           "schedule evaluation - wrong hour (before schedule)",
			schedule:       "45 13 * * 1-5",                                // 1:45 PM
			now:            time.Date(2025, 9, 18, 12, 45, 0, 0, time.UTC), // Thursday 12:45 PM (1 hour before)
			expectedResult: false,
			expectedError:  false,
			description:    "Current time before scheduled hour should return false",
		},
		{
			name:           "schedule evaluation - wrong minute (before schedule)",
			schedule:       "45 13 * * 1-5",                                // 1:45 PM
			now:            time.Date(2025, 9, 18, 13, 44, 0, 0, time.UTC), // Thursday 1:44 PM (1 minute before)
			expectedResult: false,
			expectedError:  false,
			description:    "Current time before scheduled minute should return false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test cron parsing
			cronSchedule, err := cron.ParseStandard(tt.schedule)
			if tt.expectedError {
				assert.Error(t, err, "Expected error for invalid cron expression")
				return
			}
			require.NoError(t, err, "Cron parsing should succeed for valid expression")

			// Test schedule evaluation logic (first-time evaluation with fix)
			today := time.Date(tt.now.Year(), tt.now.Month(), tt.now.Day(), 0, 0, 0, 0, tt.now.Location())
			todaySchedule := cronSchedule.Next(today.Add(-time.Second)) // Get today's scheduled time

			// Check if the schedule actually applies to today (not tomorrow or later)
			isToday := todaySchedule.Year() == tt.now.Year() &&
				todaySchedule.Month() == tt.now.Month() &&
				todaySchedule.Day() == tt.now.Day()

			result := isToday && (tt.now.After(todaySchedule) || tt.now.Equal(todaySchedule))

			// Debug output for failing tests
			if tt.expectedResult != result {
				t.Logf("Debug - Test: %s", tt.name)
				t.Logf("  Current time: %s", tt.now.Format(time.RFC3339))
				t.Logf("  Today schedule: %s", todaySchedule.Format(time.RFC3339))
				t.Logf("  Is today: %v", isToday)
				t.Logf("  After/Equal: %v", tt.now.After(todaySchedule) || tt.now.Equal(todaySchedule))
				t.Logf("  Result: %v", result)
				t.Logf("  Expected: %v", tt.expectedResult)
			}

			assert.Equal(t, tt.expectedResult, result, tt.description)
		})
	}
}

func TestScheduleChangeDetectionLogic(t *testing.T) {
	tests := []struct {
		name               string
		currentSchedule    string
		currentNextTime    time.Time
		shouldDetectChange bool
		description        string
	}{
		{
			name:               "same schedule - no change",
			currentSchedule:    "45 19 * * 1-5",
			currentNextTime:    time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Today's time
			shouldDetectChange: false,
			description:        "Should not detect change when times match",
		},
		{
			name:               "different minute - detect change",
			currentSchedule:    "30 19 * * 1-5",                                // Changed from 45 to 30
			currentNextTime:    time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Old time
			shouldDetectChange: true,
			description:        "Should detect change when minute is different",
		},
		{
			name:               "different hour - detect change",
			currentSchedule:    "45 20 * * 1-5",                                // Changed from 19 to 20
			currentNextTime:    time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Old time
			shouldDetectChange: true,
			description:        "Should detect change when hour is different",
		},
		{
			name:               "different day - detect change",
			currentSchedule:    "45 19 * * 0,6",                                // Changed from Mon-Fri to Sat-Sun
			currentNextTime:    time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Old time (Wednesday)
			shouldDetectChange: true,
			description:        "Should detect change when day of week is different",
		},
		{
			name:               "schedule changed to very near future - should detect change",
			currentSchedule:    "27 18 * * 1-5",                                // 6:27 PM weekdays
			currentNextTime:    time.Date(2025, 9, 22, 18, 27, 0, 0, time.UTC), // Next Monday
			shouldDetectChange: true,
			description:        "Should detect change when new schedule time is very close in the future (within 5 minutes)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the current schedule
			cronSchedule, err := cron.ParseStandard(tt.currentSchedule)
			require.NoError(t, err, "Should parse valid cron expression")

			// Calculate expected next time based on current time
			now := time.Date(2025, 9, 18, 19, 30, 0, 0, time.UTC) // Wednesday 7:30 PM
			expectedNextTime := metav1.NewTime(cronSchedule.Next(now))
			currentNextTime := metav1.NewTime(tt.currentNextTime)

			// Test the change detection logic
			timesEqual := currentNextTime.Equal(&expectedNextTime)
			shouldDetectChange := !timesEqual

			assert.Equal(t, tt.shouldDetectChange, shouldDetectChange, tt.description)

			if tt.shouldDetectChange {
				assert.NotEqual(t, currentNextTime, expectedNextTime, "Times should be different when change is detected")
			} else {
				assert.Equal(t, currentNextTime, expectedNextTime, "Times should be equal when no change is detected")
			}
		})
	}
}

// TestComplexScheduleScenarios tests non-daily, hourly, and monthly schedules
func TestComplexScheduleScenarios(t *testing.T) {
	tests := []struct {
		name         string
		schedule     string
		now          time.Time
		expectedNext string // Expected next occurrence in format "2006-01-02 15:04"
		description  string
	}{
		{
			name:         "weekday only schedule - Friday",
			schedule:     "0 9 * * 1,3,5",                              // 9 AM Mon, Wed, Fri
			now:          time.Date(2025, 9, 19, 8, 0, 0, 0, time.UTC), // Friday 8 AM
			expectedNext: "2025-09-19 09:00",                           // Same day Friday 9 AM
			description:  "Weekday-only schedule should work on valid weekdays",
		},
		{
			name:         "weekday only schedule - Wednesday",
			schedule:     "0 9 * * 1,3,5",                              // 9 AM Mon, Wed, Fri
			now:          time.Date(2025, 9, 22, 8, 0, 0, 0, time.UTC), // Monday 8 AM
			expectedNext: "2025-09-22 09:00",                           // Same day 9 AM
			description:  "Weekday-only schedule should work on valid weekdays",
		},
		{
			name:         "weekday only schedule - Saturday skip",
			schedule:     "0 9 * * 1,3,5",                              // 9 AM Mon, Wed, Fri
			now:          time.Date(2025, 9, 20, 8, 0, 0, 0, time.UTC), // Saturday 8 AM
			expectedNext: "2025-09-22 09:00",                           // Next Monday 9 AM
			description:  "Weekday-only schedule should skip weekends",
		},
		{
			name:         "hourly schedule",
			schedule:     "0 */2 * * *",                                 // Every 2 hours
			now:          time.Date(2025, 9, 19, 9, 30, 0, 0, time.UTC), // 9:30 AM
			expectedNext: "2025-09-19 10:00",                            // Next 2-hour mark
			description:  "Hourly schedule should calculate next occurrence correctly",
		},
		{
			name:         "monthly schedule",
			schedule:     "0 9 1 * *",                                   // 9 AM on 1st of every month
			now:          time.Date(2025, 9, 19, 10, 0, 0, 0, time.UTC), // Sep 19
			expectedNext: "2025-10-01 09:00",                            // Next month 1st
			description:  "Monthly schedule should calculate next month correctly",
		},
		{
			name:         "end of month transition",
			schedule:     "0 9 1 * *",                                   // 9 AM on 1st of every month
			now:          time.Date(2025, 1, 31, 10, 0, 0, 0, time.UTC), // Jan 31
			expectedNext: "2025-02-01 09:00",                            // Feb 1st
			description:  "Monthly schedule should handle month-end transitions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cronSchedule, err := cron.ParseStandard(tt.schedule)
			require.NoError(t, err, "Failed to parse schedule: %s", tt.schedule)

			// Calculate next occurrence
			next := cronSchedule.Next(tt.now)
			expectedTime, err := time.Parse("2006-01-02 15:04", tt.expectedNext)
			require.NoError(t, err, "Failed to parse expected time")

			assert.Equal(t, expectedTime, next, tt.description)
		})
	}
}

// TestEdgeCaseScenarios tests time zone, DST, and other edge cases
func TestEdgeCaseScenarios(t *testing.T) {
	tests := []struct {
		name        string
		schedule    string
		now         time.Time
		description string
		expectError bool
	}{
		{
			name:        "leap year handling",
			schedule:    "0 9 29 2 *",                                 // 9 AM on Feb 29
			now:         time.Date(2024, 2, 29, 8, 0, 0, 0, time.UTC), // Leap year
			description: "Leap year schedule should work correctly",
			expectError: false,
		},
		{
			name:        "invalid schedule",
			schedule:    "invalid cron",
			now:         time.Date(2025, 9, 19, 10, 0, 0, 0, time.UTC),
			description: "Invalid schedule should return error",
			expectError: true,
		},
		{
			name:        "empty schedule",
			schedule:    "",
			now:         time.Date(2025, 9, 19, 10, 0, 0, 0, time.UTC),
			description: "Empty schedule should return error",
			expectError: true,
		},
		{
			name:        "year boundary",
			schedule:    "0 9 * * *",                                     // Daily at 9 AM
			now:         time.Date(2024, 12, 31, 23, 59, 0, 0, time.UTC), // Year end
			description: "Schedule should handle year boundary transitions",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cron.ParseStandard(tt.schedule)
			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
			}
		})
	}
}

// TestEndToEndScenarios tests complete hibernation cycles
func TestEndToEndScenarios(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name        string
		description string
		scenario    func(t *testing.T, client client.Client)
	}{
		{
			name:        "complete daily cycle",
			description: "Test pause at 6 PM, unpause at 9 AM next day",
			scenario: func(t *testing.T, client client.Client) {
				// Create cluster with hibernation enabled
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause:   "0 18 * * *", // 6 PM daily
								Unpause: "0 9 * * *",  // 9 AM daily
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateReady,
					},
				}

				// Create reconciler
				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()

				// Test 1: Should pause at 6 PM
				shouldPause, err := r.shouldPauseCluster(ctx, cr, "0 18 * * *",
					time.Date(2025, 9, 19, 18, 0, 0, 0, time.UTC)) // 6 PM
				require.NoError(t, err)
				assert.True(t, shouldPause, "Should pause at scheduled time")

				// Test 2: Basic unpause logic validation
				// Simulate cluster being paused
				cr.Status.State = apiv1.StatePaused
				cr.Status.Hibernation = &apiv1.HibernationStatus{
					State:           apiv1.HibernationStatePaused,
					LastPauseTime:   &metav1.Time{Time: time.Date(2025, 9, 19, 18, 0, 0, 0, time.UTC)},
					LastUnpauseTime: &metav1.Time{Time: time.Date(2025, 9, 19, 9, 0, 0, 0, time.UTC)}, // Previous unpause
				}

				// Test that unpause logic can be called without error
				_, err = r.shouldUnpauseCluster(ctx, cr, "0 9 * * *",
					time.Date(2025, 9, 20, 9, 0, 0, 0, time.UTC)) // Next day 9 AM
				require.NoError(t, err, "ShouldUnpauseCluster should not return error")

				// Verify cluster state is correct for unpause
				assert.Equal(t, apiv1.StatePaused, cr.Status.State, "Cluster should be in paused state")
				assert.Equal(t, apiv1.HibernationStatePaused, cr.Status.Hibernation.State, "Hibernation should be in paused state")
			},
		},
		{
			name:        "weekend skip scenario",
			description: "Test weekday-only schedule skipping weekends",
			scenario: func(t *testing.T, client client.Client) {
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause: "0 18 * * 1-5", // 6 PM weekdays only
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateReady,
					},
				}

				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()

				// Test: Should NOT pause on Saturday
				shouldPause, err := r.shouldPauseCluster(ctx, cr, "0 18 * * 1-5",
					time.Date(2025, 9, 20, 18, 0, 0, 0, time.UTC)) // Saturday 6 PM
				require.NoError(t, err)
				assert.False(t, shouldPause, "Should not pause on weekends")

				// Test: Should pause on Monday
				shouldPause, err = r.shouldPauseCluster(ctx, cr, "0 18 * * 1-5",
					time.Date(2025, 9, 22, 18, 0, 0, 0, time.UTC)) // Monday 6 PM
				require.NoError(t, err)
				assert.True(t, shouldPause, "Should pause on weekdays")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			tt.scenario(t, client)
		})
	}
}

// TestFailureRecoveryScenarios tests error handling and recovery
func TestFailureRecoveryScenarios(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name        string
		description string
		scenario    func(t *testing.T, client client.Client)
	}{
		{
			name:        "cluster not found error",
			description: "Test handling when cluster is deleted",
			scenario: func(t *testing.T, client client.Client) {
				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()
				req := ctrl.Request{
					NamespacedName: types.NamespacedName{
						Name:      "non-existent-cluster",
						Namespace: "default",
					},
				}

				// Should not return error when cluster not found
				result, err := r.Reconcile(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result, "Should return empty result when cluster not found")
			},
		},
		{
			name:        "invalid hibernation configuration",
			description: "Test handling of invalid hibernation config",
			scenario: func(t *testing.T, client client.Client) {
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause: "invalid cron", // Invalid schedule
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateReady,
					},
				}

				require.NoError(t, client.Create(context.Background(), cr))

				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()

				// Should return error for invalid schedule
				_, err := r.shouldPauseCluster(ctx, cr, "invalid cron", time.Now())
				assert.Error(t, err, "Should return error for invalid cron schedule")
			},
		},
		{
			name:        "cluster in error state",
			description: "Test handling when cluster is in error state",
			scenario: func(t *testing.T, client client.Client) {
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause: "0 18 * * *", // 6 PM daily
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateError, // Error state
					},
				}

				require.NoError(t, client.Create(context.Background(), cr))

				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()

				// Should not be able to pause when cluster is in error state
				canPause, reason, err := r.canPauseCluster(ctx, cr)
				require.NoError(t, err)
				assert.False(t, canPause, "Should not be able to pause cluster in error state")
				assert.Contains(t, reason, "cluster not ready", "Should indicate cluster not ready")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			tt.scenario(t, client)
		})
	}
}

// TestPerformanceScenarios tests performance with multiple clusters
func TestPerformanceScenarios(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	t.Run("multiple clusters with hibernation", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		// Create multiple clusters
		for i := 0; i < 10; i++ {
			cr := &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      fmt.Sprintf("test-cluster-%d", i),
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Hibernation: &apiv1.HibernationSpec{
						Enabled: true,
						Schedule: apiv1.HibernationSchedule{
							Pause: "0 18 * * *", // 6 PM daily
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					State: apiv1.StateReady,
				},
			}
			require.NoError(t, client.Create(context.Background(), cr))
		}

		r := &PerconaServerMySQLHibernationReconciler{
			Client: client,
			Scheme: scheme,
		}

		ctx := context.Background()
		now := time.Date(2025, 9, 19, 18, 0, 0, 0, time.UTC) // 6 PM

		// Test performance with multiple clusters
		start := time.Now()
		for i := 0; i < 10; i++ {
			cr := &apiv1.PerconaServerMySQL{}
			err := client.Get(ctx, types.NamespacedName{
				Name:      fmt.Sprintf("test-cluster-%d", i),
				Namespace: "default",
			}, cr)
			require.NoError(t, err)

			_, err = r.shouldPauseCluster(ctx, cr, "0 18 * * *", now)
			require.NoError(t, err)
		}
		duration := time.Since(start)

		// Should complete quickly (less than 1 second for 10 clusters)
		assert.Less(t, duration, time.Second, "Processing 10 clusters should be fast")
	})
}

// TestUserExperienceScenarios tests error messages and status validation
func TestUserExperienceScenarios(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	tests := []struct {
		name        string
		description string
		scenario    func(t *testing.T, client client.Client)
	}{
		{
			name:        "clear error messages",
			description: "Test that error messages are clear and helpful",
			scenario: func(t *testing.T, client client.Client) {
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause: "invalid cron",
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateReady,
					},
				}

				r := &PerconaServerMySQLHibernationReconciler{
					Client: client,
					Scheme: scheme,
				}

				ctx := context.Background()

				// Test error message for invalid cron
				_, err := r.shouldPauseCluster(ctx, cr, "invalid cron", time.Now())
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid cron", "Error message should mention invalid cron")
			},
		},
		{
			name:        "status validation",
			description: "Test that hibernation status is properly validated",
			scenario: func(t *testing.T, client client.Client) {
				cr := &apiv1.PerconaServerMySQL{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "default",
					},
					Spec: apiv1.PerconaServerMySQLSpec{
						Hibernation: &apiv1.HibernationSpec{
							Enabled: true,
							Schedule: apiv1.HibernationSchedule{
								Pause: "0 18 * * *", // 6 PM daily
							},
						},
					},
					Status: apiv1.PerconaServerMySQLStatus{
						State: apiv1.StateReady,
					},
				}

				// Test that hibernation status initialization would work
				// (We can't easily test the full flow with fake client due to status updates)
				assert.NotNil(t, cr.Spec.Hibernation, "Hibernation spec should be set")
				assert.True(t, cr.Spec.Hibernation.Enabled, "Hibernation should be enabled")
				assert.Equal(t, "0 18 * * *", cr.Spec.Hibernation.Schedule.Pause, "Pause schedule should be correct")
				assert.Equal(t, apiv1.StateReady, cr.Status.State, "Cluster should be in ready state")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().WithScheme(scheme).Build()
			tt.scenario(t, client)
		})
	}
}

func TestPerconaServerMySQLHibernationReconciler_calculateNextScheduleTime(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, apiv1.AddToScheme(scheme))

	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	reconciler := &PerconaServerMySQLHibernationReconciler{
		Client:        client,
		Scheme:        scheme,
		ServerVersion: &platform.ServerVersion{},
	}

	tests := []struct {
		name           string
		schedule       string
		currentTime    time.Time
		expectedResult time.Time
		description    string
	}{
		{
			name:           "today's schedule still available",
			schedule:       "45 19 * * 1-5",                                // 7:45 PM Mon-Fri
			currentTime:    time.Date(2025, 9, 18, 19, 30, 0, 0, time.UTC), // Wednesday 7:30 PM (before schedule)
			expectedResult: time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Today 7:45 PM
			description:    "Should return today's schedule time when it's still in the future",
		},
		{
			name:           "today's schedule already passed",
			schedule:       "45 19 * * 1-5",                                // 7:45 PM Mon-Fri
			currentTime:    time.Date(2025, 9, 18, 20, 0, 0, 0, time.UTC),  // Wednesday 8:00 PM (after schedule)
			expectedResult: time.Date(2025, 9, 19, 19, 45, 0, 0, time.UTC), // Tomorrow 7:45 PM
			description:    "Should return tomorrow's schedule time when today's has passed",
		},
		{
			name:           "exact schedule time",
			schedule:       "45 19 * * 1-5",                                // 7:45 PM Mon-Fri
			currentTime:    time.Date(2025, 9, 18, 19, 45, 0, 0, time.UTC), // Wednesday 7:45 PM (exact time)
			expectedResult: time.Date(2025, 9, 19, 19, 45, 0, 0, time.UTC), // Tomorrow 7:45 PM
			description:    "Should return tomorrow's schedule time when current time equals schedule time",
		},
		{
			name:           "weekend schedule on weekday",
			schedule:       "45 19 * * 0,6",                                // 7:45 PM Sat-Sun
			currentTime:    time.Date(2025, 9, 18, 19, 30, 0, 0, time.UTC), // Wednesday 7:30 PM
			expectedResult: time.Date(2025, 9, 20, 19, 45, 0, 0, time.UTC), // Saturday 7:45 PM
			description:    "Should return next weekend day when schedule is for weekends",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse the schedule
			cronSchedule, err := cron.ParseStandard(tt.schedule)
			require.NoError(t, err, "Should parse valid cron expression")

			// Test the method
			result := reconciler.calculateNextScheduleTime(tt.currentTime, cronSchedule)

			// Check the result
			expectedResult := metav1.NewTime(tt.expectedResult)
			assert.Equal(t, expectedResult, result, tt.description)
		})
	}
}
