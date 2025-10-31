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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.nhat.io/grpcmock"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sversion "k8s.io/apimachinery/pkg/version"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
	vs "github.com/percona/percona-server-mysql-operator/pkg/version/service"
)

func TestReconcileVersions(t *testing.T) {
	namespace := "some-namespace"
	clusterName := "some-cluster"
	q, err := resource.ParseQuantity("1Gi")
	require.NoError(t, err)
	addr := "127.0.0.1"
	gwPort := 11000
	vsServer := fakeVersionService(addr, gwPort, false)

	require.NoError(t, vsServer.Start(t))
	defaultEndpoint := fmt.Sprintf("http://%s:%d", addr, gwPort)

	customGwPort := 13000
	customVsServer := fakeVersionService(addr, customGwPort, true)
	require.NoError(t, customVsServer.Start(t))
	customEndpoint := fmt.Sprintf("http://%s:%d", addr, customGwPort)

	tests := []struct {
		name             string
		cr               *apiv1.PerconaServerMySQL
		telemetryEnabled bool
		want             apiv1.PerconaServerMySQLStatus
		shouldErr        bool
	}{
		{
			name: "Test disabled telemetry and version upgrade",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
						VolumeSpec: &apiv1.VolumeSpec{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceStorage: q,
									},
								},
							},
						},
						PodSpec: apiv1.PodSpec{
							Size: 3,
						},
					},
					Proxy: apiv1.ProxySpec{
						HAProxy: &apiv1.HAProxySpec{
							Enabled: true,
							PodSpec: apiv1.PodSpec{
								Size: 2,
							},
						},
					},
					UpgradeOptions: apiv1.UpgradeOptions{
						Apply:                  apiv1.UpgradeStrategyDisabled,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
			},
			telemetryEnabled: false,
			want:             apiv1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test enabled telemetry and version upgrade",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
						VolumeSpec: &apiv1.VolumeSpec{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceStorage: q,
									},
								},
							},
						},
						PodSpec: apiv1.PodSpec{
							Size: 3,
						},
					},
					Proxy: apiv1.ProxySpec{
						HAProxy: &apiv1.HAProxySpec{
							Enabled: true,
							PodSpec: apiv1.PodSpec{
								Size: 2,
							},
						},
					},
					UpgradeOptions: apiv1.UpgradeOptions{
						Apply:                  apiv1.UpgradeStrategyDisabled,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
			},
			telemetryEnabled: true,
			want:             apiv1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test enabled telemetry and custom version upgrade endpoint",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
						VolumeSpec: &apiv1.VolumeSpec{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceStorage: q,
									},
								},
							},
						},
						PodSpec: apiv1.PodSpec{
							Size: 3,
						},
					},
					Proxy: apiv1.ProxySpec{
						HAProxy: &apiv1.HAProxySpec{
							Enabled: true,
							PodSpec: apiv1.PodSpec{
								Size: 2,
							},
						},
					},
					UpgradeOptions: apiv1.UpgradeOptions{
						Apply:                  apiv1.UpgradeStrategyRecommended,
						VersionServiceEndpoint: customEndpoint,
					},
				},
			},
			telemetryEnabled: true,
			shouldErr:        true,
			want:             apiv1.PerconaServerMySQLStatus{},
		},
		{
			name: "Test disabled telemetry with `recommended` upgrade strategy",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
						VolumeSpec: &apiv1.VolumeSpec{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceStorage: q,
									},
								},
							},
						},
						PodSpec: apiv1.PodSpec{
							Size: 3,
						},
					},
					Proxy: apiv1.ProxySpec{
						HAProxy: &apiv1.HAProxySpec{
							Enabled: true,
							PodSpec: apiv1.PodSpec{
								Size: 2,
							},
						},
					},
					UpgradeOptions: apiv1.UpgradeOptions{
						Apply:                  apiv1.UpgradeStrategyRecommended,
						VersionServiceEndpoint: defaultEndpoint,
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					MySQL: apiv1.StatefulAppStatus{
						Version: "database-version",
					},
					HAProxy: apiv1.StatefulAppStatus{
						Version: "haproxy-version",
					},
					BackupVersion:  "backup-version",
					ToolkitVersion: "toolkit-version",
					PMMVersion:     "pmm-version",
				},
			},
			telemetryEnabled: false,
			want: apiv1.PerconaServerMySQLStatus{
				MySQL: apiv1.StatefulAppStatus{
					Version: "mysql-version",
				},
				Orchestrator: apiv1.StatefulAppStatus{
					Version: "orchestrator-version",
				},
				Router: apiv1.StatefulAppStatus{
					Version: "router-version",
				},
				HAProxy: apiv1.StatefulAppStatus{
					Version: "haproxy-version",
				},
				ToolkitVersion: "toolkit-version",
				BackupVersion:  "backup-version",
				PMMVersion:     "pmm-version",
			},
		},
	}

	scheme := runtime.NewScheme()
	require.NoError(t, clientgoscheme.AddToScheme(scheme))
	require.NoError(t, apiv1.AddToScheme(scheme))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.telemetryEnabled {
				t.Setenv("DISABLE_TELEMETRY", "false")
			} else {
				t.Setenv("DISABLE_TELEMETRY", "true")
			}
			t.Setenv("PERCONA_VS_FALLBACK_URI", defaultEndpoint)

			cb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.cr).WithStatusSubresource(tt.cr)
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

			cr, err := k8s.GetCRWithDefaults(t.Context(), r.Client, client.ObjectKeyFromObject(tt.cr), r.ServerVersion)
			require.NoError(t, err)

			err = r.reconcileVersions(t.Context(), cr)
			if tt.shouldErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)

			require.NoError(t, r.Get(t.Context(), client.ObjectKeyFromObject(cr), cr))

			assert.Equal(t, tt.want, cr.Status)
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
		cr   *apiv1.PerconaServerMySQL
		want vs.DepVersion
	}{
		{
			name: "Test minimal CR",
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      clusterName,
					Namespace: namespace,
					UID:       types.UID("custom-resource-uid"),
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					Backup: &apiv1.BackupSpec{
						Image: "some-image",
					},
					MySQL: apiv1.MySQLSpec{
						ClusterType: apiv1.ClusterTypeGR,
						VolumeSpec: &apiv1.VolumeSpec{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
								Resources: corev1.VolumeResourceRequirements{
									Requests: map[corev1.ResourceName]resource.Quantity{
										corev1.ResourceStorage: q,
									},
								},
							},
						},
						PodSpec: apiv1.PodSpec{
							Size: 3,
						},
					},
					Proxy: apiv1.ProxySpec{
						HAProxy: &apiv1.HAProxySpec{
							Enabled: true,
							PodSpec: apiv1.PodSpec{
								Size: 2,
							},
						},
					},
				},
				Status: apiv1.PerconaServerMySQLStatus{
					MySQL: apiv1.StatefulAppStatus{
						Version: "database-version",
					},
					HAProxy: apiv1.StatefulAppStatus{
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
	if err := apiv1.AddToScheme(scheme); err != nil {
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
	case string(apiv1.UpgradeStrategyDisabled), string(apiv1.UpgradeStrategyNever):
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
		OperatorVersion:   version.Version(),
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
