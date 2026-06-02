package psclusterset

//go:generate go tool mockery --name=ClusterSetManager --case=snake --output=./mock --outpkg=mock
//go:generate go tool mockery --name=EventRecorder --case=snake --output=./mock --outpkg=mock --srcpkg=k8s.io/client-go/tools/record
