package testutil

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

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

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
		return nil, errors.Errorf("Have: %v; Want: %v", *have, *want)
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

func FakeVersionService(addr string, port int, gwport int, unimplemented bool) *fakeVS {
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

	go func() {
		if err := gwServer.ListenAndServe(); err != nil {
			t.Error("failed to serve gRPC-Gateway", err)
		}
	}()
	return nil
}
