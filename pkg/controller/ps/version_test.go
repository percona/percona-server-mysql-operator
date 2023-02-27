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
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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
	port := 10000
	gwPort := 11000
	vsServer := fakeVersionService(addr, port, gwPort, false)
	if err := vsServer.Start(t); err != nil {
		t.Fatal(err, "failed to start fake version service server")
	}
	defaultEndpoint := fmt.Sprintf("http://%s:%d", addr, gwPort)

	customPort := 12000
	customGwPort := 13000
	customVsServer := fakeVersionService(addr, customPort, customGwPort, true)
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
	s := fakeVersionService(addr, port, gwPort, false)
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
	port          int
	gwPort        int
	unimplemented bool
}

func (vs *fakeVS) Product(ctx context.Context, req *pbVersion.ProductRequest) (*pbVersion.ProductResponse, error) {
	if vs.unimplemented {
		return nil, errors.New("unimplemented")
	}
	return &pbVersion.ProductResponse{}, nil
}

func (vs *fakeVS) Operator(ctx context.Context, req *pbVersion.OperatorRequest) (*pbVersion.OperatorResponse, error) {
	if vs.unimplemented {
		return nil, errors.New("unimplemented")
	}
	return &pbVersion.OperatorResponse{}, nil
}

func (vs *fakeVS) Apply(_ context.Context, req *pbVersion.ApplyRequest) (*pbVersion.VersionResponse, error) {
	if vs.unimplemented {
		return nil, errors.New("unimplemented")
	}
	switch req.Apply {
	case string(apiv1alpha1.UpgradeStrategyDisabled), string(apiv1alpha1.UpgradeStrategyNever):
		return &pbVersion.VersionResponse{}, nil
	}

	have := &pbVersion.ApplyRequest{
		BackupVersion:     req.GetBackupVersion(),
		CustomResourceUid: req.GetCustomResourceUid(),
		DatabaseVersion:   req.GetDatabaseVersion(),
		KubeVersion:       req.GetKubeVersion(),
		NamespaceUid:      req.GetNamespaceUid(),
		OperatorVersion:   req.GetOperatorVersion(),
		Platform:          req.GetPlatform(),
		Product:           req.GetProduct(),
	}
	want := &pbVersion.ApplyRequest{
		BackupVersion:     "backup-version",
		CustomResourceUid: "custom-resource-uid",
		DatabaseVersion:   "database-version",
		KubeVersion:       "kube-version",
		OperatorVersion:   version.Version,
		Product:           "ps-operator",
		Platform:          string(platform.PlatformKubernetes),
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
				},
			},
		},
	}, nil
}

func fakeVersionService(addr string, port int, gwport int, unimplemented bool) *fakeVS {
	return &fakeVS{
		addr:          addr,
		port:          port,
		gwPort:        gwport,
		unimplemented: unimplemented,
	}
}

func (vs *fakeVS) Start(t *testing.T) error {
	s := grpc.NewServer()
	pbVersion.RegisterVersionServiceServer(s, vs)

	lis, err := net.Listen("tcp", fmt.Sprintf("%s:%d", vs.addr, vs.port))
	if err != nil {
		return errors.Wrap(err, "failed to listen interface")
	}
	go func() {
		if err := s.Serve(lis); err != nil {
			t.Error(err, "failed to serve grpc server")
		}
	}()

	conn, err := grpc.DialContext(
		context.Background(),
		fmt.Sprintf("dns:///%s", fmt.Sprintf("%s:%d", vs.addr, vs.port)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return errors.Wrap(err, "failed to dial server")
	}

	gwmux := gwRuntime.NewServeMux()
	err = pbVersion.RegisterVersionServiceHandler(context.Background(), gwmux, conn)
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
