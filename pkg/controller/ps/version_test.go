package ps

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"testing"

	pbVersion "github.com/Percona-Lab/percona-version-service/versionpb"
	gwRuntime "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/pkg/errors"
	"go.nhat.io/grpcmock"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
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
	gwPort := 11000
	vsServer := fakeVersionService(addr, gwPort, false)
	if err := vsServer.Start(t); err != nil {
		t.Fatal(err, "failed to start fake version service server")
	}
	defaultEndpoint := fmt.Sprintf("http://%s:%d", addr, gwPort)

	customGwPort := 13000
	customVsServer := fakeVersionService(addr, customGwPort, true)
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
					HAProxy: apiv1alpha1.StatefulAppStatus{
						Version: "haproxy-version",
					},
					BackupVersion:  "backup-version",
					ToolkitVersion: "toolkit-version",
					PMMVersion:     "pmm-version",
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
				HAProxy: apiv1alpha1.StatefulAppStatus{
					Version: "haproxy-version",
				},
				ToolkitVersion: "toolkit-version",
				BackupVersion:  "backup-version",
				PMMVersion:     "pmm-version",
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

func TestGetVersion(t *testing.T) {
	ctx := context.Background()

	namespace := "some-namespace"
	clusterName := "some-cluster"
	q, err := resource.ParseQuantity("1Gi")
	if err != nil {
		t.Fatal(err)
	}
	addr := "127.0.0.1"
	gwPort := 15000
	s := fakeVersionService(addr, gwPort, false)
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
					HAProxy: apiv1alpha1.StatefulAppStatus{
						Version: "haproxy-version",
					},
					BackupVersion:  "backup-version",
					ToolkitVersion: "toolkit-version",
					PMMVersion:     "pmm-version",
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
				ToolkitImage:        "toolkit-image",
				ToolkitVersion:      "toolkit-version",
				HAProxyImage:        "haproxy-image",
				HAProxyVersion:      "haproxy-version",
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

type fakeVS struct {
	addr          string
	gwPort        int
	unimplemented bool
}

func (vs *fakeVS) Apply(_ context.Context, req any) (any, error) {
	if vs.unimplemented {
		return nil, errors.New("unimplemented")
	}
	r := req.(*pbVersion.ApplyRequest)
	switch r.Apply {
	case string(apiv1alpha1.UpgradeStrategyDisabled), string(apiv1alpha1.UpgradeStrategyNever):
		return &pbVersion.VersionResponse{}, nil
	}

	have := &pbVersion.ApplyRequest{
		BackupVersion:     r.GetBackupVersion(),
		CustomResourceUid: r.GetCustomResourceUid(),
		DatabaseVersion:   r.GetDatabaseVersion(),
		KubeVersion:       r.GetKubeVersion(),
		NamespaceUid:      r.GetNamespaceUid(),
		OperatorVersion:   r.GetOperatorVersion(),
		Platform:          r.GetPlatform(),
		Product:           r.GetProduct(),
		HaproxyVersion:    r.GetHaproxyVersion(),
		PmmVersion:        r.GetPmmVersion(),
	}
	want := &pbVersion.ApplyRequest{
		BackupVersion:     "backup-version",
		CustomResourceUid: "custom-resource-uid",
		DatabaseVersion:   "database-version",
		KubeVersion:       "kube-version",
		OperatorVersion:   version.Version,
		Product:           "ps-operator",
		Platform:          string(platform.PlatformKubernetes),
		HaproxyVersion:    "haproxy-version",
		PmmVersion:        "pmm-version",
	}

	if !reflect.DeepEqual(have, want) {
		return nil, errors.Errorf("Have: %v; Want: %v", have, want)
	}

	return &pbVersion.VersionResponse{
		Versions: []*pbVersion.OperatorVersion{
			{
				Matrix: &pbVersion.VersionMatrix{
					Mysql: map[string]*pbVersion.Version{
						"mysql-version": {
							ImagePath: "mysql-image",
						},
					},
					Backup: map[string]*pbVersion.Version{
						"backup-version": {
							ImagePath: "backup-image",
						},
					},
					Pmm: map[string]*pbVersion.Version{
						"pmm-version": {
							ImagePath: "pmm-image",
						},
					},
					Orchestrator: map[string]*pbVersion.Version{
						"orchestrator-version": {
							ImagePath: "orchestrator-image",
						},
					},
					Router: map[string]*pbVersion.Version{
						"router-version": {
							ImagePath: "router-image",
						},
					},
					Haproxy: map[string]*pbVersion.Version{
						"haproxy-version": {
							ImagePath: "haproxy-image",
						},
					},
					Toolkit: map[string]*pbVersion.Version{
						"toolkit-version": {
							ImagePath: "toolkit-image",
						},
					},
				},
			},
		},
	}, nil
}

func fakeVersionService(addr string, gwport int, unimplemented bool) *fakeVS {
	return &fakeVS{
		addr:          addr,
		gwPort:        gwport,
		unimplemented: unimplemented,
	}
}

type mockClientConn struct {
	dialer grpcmock.ContextDialer
}

func (m *mockClientConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	return grpcmock.InvokeUnary(ctx, method, args, reply, grpcmock.WithInsecure(), grpcmock.WithCallOptions(opts...), grpcmock.WithContextDialer(m.dialer))
}
func (m *mockClientConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unimplemented")
}

func (vs *fakeVS) Start(t *testing.T) error {
	_, d := grpcmock.MockServerWithBufConn(
		grpcmock.RegisterServiceFromInstance("version.VersionService", (*pbVersion.VersionServiceServer)(nil)),
		func(s *grpcmock.Server) {
			s.ExpectUnary("/version.VersionService/Apply").Run(vs.Apply)
		},
	)(t)

	gwmux := gwRuntime.NewServeMux()
	err := pbVersion.RegisterVersionServiceHandlerClient(context.Background(), gwmux, pbVersion.NewVersionServiceClient(&mockClientConn{d}))
	if err != nil {
		return errors.Wrap(err, "failed to register gateway")
	}
	gwServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", vs.addr, vs.gwPort),
		Handler: gwmux,
	}
	gwLis, err := net.Listen("tcp", gwServer.Addr)
	if err != nil {
		return errors.Wrap(err, "failed to listen gateway")
	}
	go func() {
		if err := gwServer.Serve(gwLis); err != nil {
			t.Error("failed to serve gRPC-Gateway", err)
		}
	}()

	return nil
}
