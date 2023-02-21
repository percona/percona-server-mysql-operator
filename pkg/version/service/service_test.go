package service_test

import (
	"context"
	"fmt"
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
	"github.com/percona/percona-server-mysql-operator/controllers"
	"github.com/percona/percona-server-mysql-operator/internal/testutil"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
)

func TestGetVersion(t *testing.T) {
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
	s := testutil.FakeVersionService(addr, port, gwPort, false)
	if err := s.Start(t); err != nil {
		t.Fatal(err, "failed to start fake version service server")
	}

	tests := []struct {
		name string
		cr   *apiv1alpha1.PerconaServerMySQL
		want vs.DepVersion
	}{
		{
			name: "Test minimal CR",
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
				},
				Status: apiv1alpha1.PerconaServerMySQLStatus{
					MySQL: apiv1alpha1.StatefulAppStatus{
						Version: "database-version",
					},
					BackupVersion: "backup-version",
				},
			},
			want: vs.DepVersion{
				PSImage:             "mysql-image",
				PSVersion:           "mysql-version",
				BackupImage:         "backup-image",
				BackupVersion:       "backup-version",
				OrchestratorImage:   "orchestrator-image",
				OrchestratorVersion: "orchestrator-version",
				RouterImage:         "router-image",
				RouterVersion:       "router-version",
				PMMImage:            "pmm-image",
				PMMVersion:          "pmm-version",
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
			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr)

			r := controllers.PerconaServerMySQLReconciler{
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

			dv, err := vs.GetVersion(ctx, cr, fmt.Sprintf("http://%s:%d", addr, gwPort), r.ServerVersion)
			if err != nil {
				t.Fatal(err, "failed to get version")
			}

			if dv != tt.want {
				t.Fatalf("got %v, want %v", dv, tt.want)
			}
		})
	}
}
