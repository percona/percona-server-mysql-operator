package ps

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/internal/testutil"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
)

func TestReconcileVersions(t *testing.T) {
	ctx := context.Background()

	namespace := "some-namespace"
	clusterName := "some-cluster"
	q, err := resource.ParseQuantity("1Gi")
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1"
	port := 10000
	gwPort := 11000
	vsServer := testutil.FakeVersionService(addr, port, gwPort, false)
	if err := vsServer.Start(t); err != nil {
		t.Fatal(err, "failed to start fake version service server")
	}
	defaultEndpoint := fmt.Sprintf("http://%s:%d", addr, gwPort)

	customPort := 12000
	customGwPort := 13000
	customVsServer := testutil.FakeVersionService(addr, customPort, customGwPort, true)
	if err := customVsServer.Start(t); err != nil {
		t.Fatal(err, "failed to start custom fake version service server")
	}
	customEndpoint := fmt.Sprintf("http://%s:%d", addr, customGwPort)

	tests := []struct {
		name             string
		cr               *apiv1alpha1.PerconaServerMySQL
		telemetryEnabled bool
		want             apiv1alpha1.PerconaServerMySQLStatus
		shouldErr        bool
	}{
		{
			name: "Test disabled telemetry and version upgrade",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
					UpgradeOptions: apiv1alpha1.UpgradeOptions{
						Apply:                  apiv1alpha1.UpgradeStrategyDisabled,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
			},
			telemetryEnabled: false,
			want:             apiv1alpha1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test enabled telemetry and version upgrade",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
					UpgradeOptions: apiv1alpha1.UpgradeOptions{
						Apply:                  apiv1alpha1.UpgradeStrategyDisabled,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
			},
			telemetryEnabled: true,
			want:             apiv1alpha1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test enabled telemetry and custom version upgrade endpoint",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
					UpgradeOptions: apiv1alpha1.UpgradeOptions{
						Apply:                  apiv1alpha1.UpgradeStrategyRecommended,
						VersionServiceEndpoint: customEndpoint,
					},
				},
			},
			telemetryEnabled: true,
			shouldErr:        true,
			want:             apiv1alpha1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test disabled telemetry with `recommended` upgrade strategy",
			cr: &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					Backup: &apiv1alpha1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1alpha1.MySQLSpec{
						PodSpec: apiv1alpha1.PodSpec{
							VolumeSpec: &apiv1alpha1.VolumeSpec{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
									Resources: corev1.ResourceRequirements{
										Requests: map[corev1.ResourceName]resource.Quantity{
											corev1.ResourceStorage: q,
										},
									},
								},
							},
						},
					},
					UpgradeOptions: apiv1alpha1.UpgradeOptions{
						Apply:                  apiv1alpha1.UpgradeStrategyRecommended,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
				Status: apiv1alpha1.PerconaServerMySQLStatus{
					MySQL: apiv1alpha1.StatefulAppStatus{
						Version: "database-version",
					},
					BackupVersion: "backup-version",
				},
			},
			telemetryEnabled: false,
			want: apiv1alpha1.PerconaServerMySQLStatus{
				MySQL: apiv1alpha1.StatefulAppStatus{
					Version: "mysql-version",
				},
				Orchestrator: apiv1alpha1.StatefulAppStatus{
					Version: "orchestrator-version",
				},
				Router: apiv1alpha1.StatefulAppStatus{
					Version: "router-version",
				},
				BackupVersion: "backup-version",
				PMMVersion:    "pmm-version",
			},
		},
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add client-go scheme")
	}
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err, "failed to add apis scheme")
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.telemetryEnabled {
				t.Setenv("DISABLE_TELEMETRY", "false")
			} else {
				t.Setenv("DISABLE_TELEMETRY", "true")
			}
			t.Setenv("PERCONA_VS_FALLBACK_URI", defaultEndpoint)

			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr)
			r := PerconaServerMySQLReconciler{
				Client: cb.Build(),
				Scheme: scheme,
				ServerVersion: &platform.ServerVersion{
					Platform: platform.PlatformKubernetes,
					Info: k8sversion.Info{
						GitVersion: "kube-version",
					},
				},
			}

			cr, err := k8s.GetCRWithDefaults(ctx, r.Client, types.NamespacedName{Name: tt.cr.Name, Namespace: tt.cr.Namespace}, r.ServerVersion)
			if err != nil {
				t.Fatal(err, "failed to get cr")
			}

			if err := r.reconcileVersions(ctx, cr); err != nil {
				if tt.shouldErr {
					return
				}
				t.Fatal(err, "failed to reconcile versions")
			}

			if !reflect.DeepEqual(cr.Status, tt.want) {
				t.Fatalf("got %v, want %v", cr.Status, tt.want)
			}
		})
	}
}
