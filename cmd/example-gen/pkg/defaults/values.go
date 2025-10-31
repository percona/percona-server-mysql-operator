package defaults

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

const (
	ImageInitContainer = "perconalab/percona-server-mysql-operator:main"
	ImageMySQL         = "perconalab/percona-server-mysql-operator:main-psmysql8.4"
	ImageHAProxy       = "perconalab/percona-server-mysql-operator:main-haproxy"
	ImageRouter        = "perconalab/percona-server-mysql-operator:main-router8.4"
	ImageOrchestrator  = "perconalab/percona-server-mysql-operator:main-orchestrator"
	ImagePMM           = "perconalab/pmm-client:3-dev-latest"
	ImageBackup        = "perconalab/percona-server-mysql-operator:main-backup8.4"
	ImageToolkit       = "perconalab/percona-server-mysql-operator:main-toolkit"
)

const (
	NameCluster = "ps-cluster1"
	NameBackup  = "backup1"
	NameRestore = "restore1"
)

var (
	Labels = map[string]string{
		"rack": "rack-22",
	}
	Annotations = map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
		"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
	}
	NodeSelector = map[string]string{
		"topology.kubernetes.io/zone": "us-east-1a",
	}
	VerifyTLS         = ptr.To(true)
	RuntimeClassName  = ptr.To("image-rc")
	SchedulerName     = "default-scheduler"
	PriorityClassName = "high-priority"
	SourcePod         = mysql.PodName(&apiv1.PerconaServerMySQL{ObjectMeta: metav1.ObjectMeta{Name: NameCluster}}, 1)
)
