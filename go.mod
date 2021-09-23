module github.com/percona/percona-server-mysql-operator

go 1.16

require (
	github.com/go-logr/logr v0.4.0
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.13.0
	github.com/openark/golib v0.0.0-20210531070646-355f37940af8 // indirect
	github.com/openark/orchestrator v3.2.5+incompatible // indirect
	github.com/pkg/errors v0.9.1
	gopkg.in/gcfg.v1 v1.2.3 // indirect
	gopkg.in/warnings.v0 v0.1.2 // indirect
	k8s.io/api v0.21.2
	k8s.io/apimachinery v0.21.2
	k8s.io/client-go v0.21.2
	sigs.k8s.io/controller-runtime v0.9.2
)
