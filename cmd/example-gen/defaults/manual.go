package defaults

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

// ManualCluster sets default values for empty fields in example cr.yaml
// that are not set by CheckNSetDefaults and cannot be set via PresetDefaults.
func ManualCluster(cr *apiv1.PerconaServerMySQL) {
	cr.Spec.Unsafe = apiv1.UnsafeFlags{
		MySQLSize:        false,
		Proxy:            false,
		ProxySize:        false,
		Orchestrator:     false,
		OrchestratorSize: false,
	}
	cr.Spec.Pause = true
	cr.Spec.CRVersion = version.Version()
	cr.Spec.VolumeExpansionEnabled = true
	cr.Spec.UpdateStrategy = "SmartUpdate"
	cr.Spec.InitContainer.Image = "perconalab/percona-server-mysql-operator:main"
	cr.Spec.IgnoreAnnotations = []string{"service.beta.kubernetes.io/aws-load-balancer-backend-protocol"}
	cr.Spec.IgnoreLabels = []string{"rack"}

	mysqlDefaults(&cr.Spec.MySQL)
	haproxyDefaults(cr.Spec.Proxy.HAProxy)
	routerDefaults(cr.Spec.Proxy.Router)
	orchestratorDefaults(&cr.Spec.Orchestrator)
	pmmDefaults(cr.Spec.PMM)
	toolkitDefaults(cr.Spec.Toolkit)
	backupDefaults(cr.Spec.Backup)
}

func mysqlDefaults(spec *apiv1.MySQLSpec) {
	spec.AutoRecovery = true
	spec.Size = 3
	spec.Image = "perconalab/percona-server-mysql-operator:main-psmysql8.4"
	spec.RuntimeClassName = ptr.To("image-rc")
	spec.Env = []corev1.EnvVar{
		{
			Name:  "BOOTSTRAP_READ_TIMEOUT",
			Value: "600",
		},
	}
	spec.EnvFrom = []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "mysql-env-secret",
				},
			},
		},
	}
	spec.Labels = map[string]string{
		"rack": "rack-22",
	}
	spec.Annotations = map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
		"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
	}

	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("4Gi"),
		},
	}
	spec.VolumeSpec = nil
	spec.Affinity = nil
	spec.ExposePrimary.Enabled = true
	spec.TerminationGracePeriodSeconds = ptr.To(int64(600))
	spec.Configuration = `max_connections=250
innodb_buffer_pool_size={{containerMemoryLimit * 3/4}}`
	spec.PriorityClassName = "high-priority"

	spec.Sidecars = []corev1.Container{
		{
			Name:  "noop-memory",
			Image: "busybox",
			Command: []string{
				"sleep", "30d",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "memory-vol",
					MountPath: "/var/log/app/memory",
				},
			},
			LivenessProbe:   &corev1.Probe{},
			ReadinessProbe:  &corev1.Probe{},
			StartupProbe:    &corev1.Probe{},
			Lifecycle:       &corev1.Lifecycle{},
			SecurityContext: &corev1.SecurityContext{},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("16M"),
				},
			},
		},
		{
			Name:  "noop-pvc",
			Image: "busybox",
			Command: []string{
				"sleep", "30d",
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "memory-vol",
					MountPath: "/var/log/app/memory",
				},
			},
			LivenessProbe:   &corev1.Probe{},
			ReadinessProbe:  &corev1.Probe{},
			StartupProbe:    &corev1.Probe{},
			Lifecycle:       &corev1.Lifecycle{},
			SecurityContext: &corev1.SecurityContext{},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceMemory: resource.MustParse("16M"),
				},
			},
		},
	}
	spec.SidecarVolumes = []corev1.Volume{
		{
			Name: "memory-vol",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium: corev1.StorageMediumMemory,
				},
			},
		},
	}

	spec.SchedulerName = "default-scheduler"
	spec.PodSecurityContext = &corev1.PodSecurityContext{
		FSGroup:            ptr.To(int64(1001)),
		SupplementalGroups: []int64{1001, 1002, 1003},
	}
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
	spec.NodeSelector = map[string]string{
		"topology.kubernetes.io/zone": "us-east-1a",
	}
}

func haproxyDefaults(spec *apiv1.HAProxySpec) {
	spec.Image = "perconalab/percona-server-mysql-operator:main-haproxy"
	spec.Enabled = true
	spec.Size = 3
	spec.RuntimeClassName = ptr.To("image-rc")
	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourceCPU:    resource.MustParse("700m"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourceCPU:    resource.MustParse("600m"),
		},
	}
	spec.PriorityClassName = "high-priority"
	spec.Env = []corev1.EnvVar{
		{
			Name:  "HA_CONNECTION_TIMEOUT",
			Value: "600",
		},
	}
	spec.EnvFrom = []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "haproxy-env-secret",
				},
			},
		},
	}
	spec.Labels = map[string]string{
		"rack": "rack-22",
	}
	spec.Annotations = map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
		"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
	}
	spec.SchedulerName = "default-scheduler"
	spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
	spec.Configuration = `#      the actual default configuration file can be found here https://github.com/percona/percona-server-mysql-operator/blob/main/build/haproxy-global.cfg

global
  maxconn 2048
  external-check
  insecure-fork-wanted
  stats socket /etc/haproxy/mysql/haproxy.sock mode 600 expose-fd listeners level admin

defaults
  default-server init-addr last,libc,none
  log global
  mode tcp
  retries 10
  timeout client 28800s
  timeout connect 100500
  timeout server 28800s

frontend mysql-primary-in
  bind *:3309 accept-proxy
  bind *:3306
  mode tcp
  option clitcpka
  default_backend mysql-primary

frontend mysql-replicas-in
  bind *:3307
  mode tcp
  option clitcpka
  default_backend mysql-replicas

frontend stats
  bind *:8404
  mode http
  http-request use-service prometheus-exporter if { path /metrics }`
	spec.NodeSelector = map[string]string{
		"topology.kubernetes.io/zone": "us-east-1a",
	}
}

func routerDefaults(spec *apiv1.MySQLRouterSpec) {
	spec.Enabled = true
	spec.Size = 3
	spec.Image = "perconalab/percona-server-mysql-operator:main-router8.4"
	spec.RuntimeClassName = ptr.To("image-rc")
	spec.PriorityClassName = "high-priority"
	spec.Labels = map[string]string{
		"rack": "rack-22",
	}
	spec.Annotations = map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
		"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
	}
	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256M"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256M"),
		},
	}
	spec.Env = []corev1.EnvVar{
		{
			Name:  "ROUTER_ENV",
			Value: "VALUE",
		},
	}
	spec.EnvFrom = []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "router-env-secret",
				},
			},
		},
	}
	spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	spec.Configuration = `[default]
logging_folder=/tmp/router/log
[logger]
level=DEBUG`
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
	spec.Ports = []corev1.ServicePort{
		{
			Name: "http",
			Port: 8443,
		},
		{
			Name:       "rw-default",
			Port:       3306,
			TargetPort: intstr.FromInt(6446),
		},
		{
			Name: "read-write",
			Port: 6446,
		},
		{
			Name: "read-only",
			Port: 6447,
		},
		{
			Name: "x-read-write",
			Port: 6448,
		},
		{
			Name: "x-read-only",
			Port: 6449,
		},
		{
			Name: "x-default",
			Port: 33060,
		},
		{
			Name: "rw-admin",
			Port: 33062,
		},
		{
			Name:       "custom-port",
			Port:       1111,
			TargetPort: intstr.FromInt(6446),
		},
	}
	spec.SchedulerName = "default-scheduler"
	spec.NodeSelector = map[string]string{
		"topology.kubernetes.io/zone": "us-east-1a",
	}
}

func orchestratorDefaults(spec *apiv1.OrchestratorSpec) {
	spec.Enabled = true
	spec.Image = "perconalab/percona-server-mysql-operator:main-orchestrator"
	spec.Size = 3
	spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	spec.Affinity = nil
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
	spec.PriorityClassName = "high-priority"
	spec.RuntimeClassName = ptr.To("image-rc")
	spec.Env = []corev1.EnvVar{
		{
			Name:  "ORC_ENV",
			Value: "VALUE",
		},
	}
	spec.EnvFrom = []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "orc-env-secret",
				},
			},
		},
	}
	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256M"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("128M"),
		},
	}
	spec.Labels = map[string]string{
		"rack": "rack-22",
	}
	spec.Annotations = map[string]string{
		"service.beta.kubernetes.io/aws-load-balancer-backend-protocol": "tcp",
		"service.beta.kubernetes.io/aws-load-balancer-type":             "nlb",
	}
	spec.SchedulerName = "default-scheduler"
	spec.NodeSelector = map[string]string{
		"topology.kubernetes.io/zone": "us-east-1a",
	}
}

func pmmDefaults(spec *apiv1.PMMSpec) {
	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256M"),
			corev1.ResourceCPU:    resource.MustParse("400m"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("150M"),
			corev1.ResourceCPU:    resource.MustParse("300m"),
		},
	}

	spec.Image = "perconalab/pmm-client:3-dev-latest"
	spec.ServerHost = "monitoring-service"
	spec.MySQLParams = "PMM_ADMIN_CUSTOM_PARAMS"
}

func backupDefaults(spec *apiv1.BackupSpec) {
	spec.Enabled = true
	spec.SourcePod = "ps-cluster1-mysql-1"
	spec.Image = "perconalab/percona-server-mysql-operator:main-backup8.4"
	spec.Schedule = []apiv1.BackupSchedule{
		{
			Name:        "sat-night-backup",
			Schedule:    "0 0 * * 6",
			Keep:        3,
			StorageName: "s3-us-west",
		},
		{
			Name:        "daily-backup",
			Schedule:    "0 0 * * *",
			Keep:        5,
			StorageName: "s3",
		},
	}
	spec.BackoffLimit = ptr.To(int32(6))
	spec.Storages = map[string]*apiv1.BackupStorageSpec{
		"azure-blob": {
			Type: apiv1.BackupStorageAzure,
			Azure: &apiv1.BackupStorageAzureSpec{
				ContainerName:     "CONTAINER-NAME",
				Prefix:            "PREFIX-NAME",
				CredentialsSecret: "SECRET-NAME",
				EndpointURL:       "https://accountName.blob.core.windows.net",
				StorageClass:      "Cool",
			},
		},
		"s3-us-west": {
			Type: apiv1.BackupStorageS3,
			S3: &apiv1.BackupStorageS3Spec{
				Bucket:            "S3-BACKUP-BUCKET-NAME-HERE",
				Prefix:            "PREFIX_NAME",
				CredentialsSecret: "ps-cluster1-s3-credentials",
				Region:            "us-west-2",
				EndpointURL:       "https://s3.amazonaws.com",
			},
			Annotations: map[string]string{
				"testName": "scheduled-backup",
			},
			Labels: map[string]string{
				"backupWorker": "True",
			},
			PriorityClassName: "high-priority",
			RuntimeClassName:  ptr.To("image-rc"),
			SchedulerName:     "default-scheduler",
			VerifyTLS:         ptr.To(true),
			NodeSelector: map[string]string{
				"topology.kubernetes.io/zone": "us-east-1a",
			},
		},
	}
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
}

func toolkitDefaults(spec *apiv1.ToolkitSpec) {
	spec.Image = "perconalab/percona-server-mysql-operator:main-toolkit"
	spec.Resources = corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("256M"),
			corev1.ResourceCPU:    resource.MustParse("400m"),
		},
		Requests: corev1.ResourceList{
			corev1.ResourceMemory: resource.MustParse("150M"),
			corev1.ResourceCPU:    resource.MustParse("100m"),
		},
	}
	spec.Env = []corev1.EnvVar{
		{
			Name:  "TOOLKIT_ENV",
			Value: "VALUE",
		},
	}
	spec.EnvFrom = []corev1.EnvFromSource{
		{
			SecretRef: &corev1.SecretEnvSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: "toolkit-env-secret",
				},
			},
		},
	}
}
