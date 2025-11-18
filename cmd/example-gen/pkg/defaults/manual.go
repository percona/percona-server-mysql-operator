package defaults

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/version"
)

// ManualCluster sets default values for empty fields in example cr.yaml
// that are not set by CheckNSetDefaults and cannot be set via PresetDefaults.
func ManualCluster(cr *apiv1.PerconaServerMySQL) {
	cr.Spec.CRVersion = version.Version()
	cr.Spec.UpdateStrategy = "SmartUpdate"
	cr.Spec.InitContainer.Image = ImageInitContainer
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
	podSpecDefaults(&spec.PodSpec, ImageMySQL, resources("2Gi", "", "4Gi", ""), configurationMySQL, 600, envList("BOOTSTRAP_READ_TIMEOUT", "600"), envFromList("mysql-env-secret"))

	spec.AutoRecovery = true
	spec.VolumeSpec = nil
	spec.ExposePrimary.Enabled = true

	c := func(name string) corev1.Container {
		return corev1.Container{
			Name:    name,
			Image:   "busybox",
			Command: []string{"sleep", "30d"},
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
			Resources:       resources("16M", "", "", ""),
		}
	}
	spec.Sidecars = []corev1.Container{
		c("noop-memory"),
		c("noop-pvc"),
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
}

func haproxyDefaults(spec *apiv1.HAProxySpec) {
	podSpecDefaults(&spec.PodSpec, ImageHAProxy, resources("1Gi", "600m", "1Gi", "700m"), configurationHAProxy, 30, envList("HA_CONNECTION_TIMEOUT", "600"), envFromList("haproxy-env-secret"))

	spec.Enabled = true
}

func routerDefaults(spec *apiv1.MySQLRouterSpec) {
	podSpecDefaults(&spec.PodSpec, ImageRouter, resources("256M", "", "256M", ""), configurationRouter, 30, envList("ROUTER_ENV", "VALUE"), envFromList("router-env-secret"))

	spec.Enabled = false
	p := func(name string, port int32, targetPort int) corev1.ServicePort {
		servicePort := corev1.ServicePort{Name: name, Port: port}
		if targetPort != 0 {
			servicePort.TargetPort = intstr.FromInt(targetPort)
		}
		return servicePort
	}
	spec.Ports = []corev1.ServicePort{
		p("http", 8443, 0),
		p("rw-default", 3306, 6446),
		p("read-write", 6446, 0),
		p("read-only", 6447, 0),
		p("x-read-write", 6448, 0),
		p("x-read-only", 6449, 0),
		p("x-default", 33060, 0),
		p("rw-admin", 33062, 0),
		p("custom-port", 1111, 6446),
	}
}

func orchestratorDefaults(spec *apiv1.OrchestratorSpec) {
	podSpecDefaults(&spec.PodSpec, ImageOrchestrator, resources("128M", "", "256M", ""), "", 30, envList("ORC_ENV", "VALUE"), envFromList("orc-env-secret"))

	spec.Enabled = false
	spec.ServiceAccountName = "percona-server-mysql-operator-orchestrator"
	spec.PodSecurityContext = &corev1.PodSecurityContext{
		SupplementalGroups: []int64{1001},
	}
}

func pmmDefaults(spec *apiv1.PMMSpec) {
	spec.Image = ImagePMM
	spec.Resources = resources("150M", "300m", "256M", "400m")
	spec.ServerHost = "monitoring-service"
	spec.MySQLParams = "PMM_ADMIN_CUSTOM_PARAMS"
}

func backupDefaults(spec *apiv1.BackupSpec) {
	spec.Image = ImageBackup
	spec.Enabled = true
	spec.SourcePod = SourcePod
	spec.ServiceAccountName = "some-service-account"
	spec.BackoffLimit = ptr.To(int32(6))
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
				CredentialsSecret: fmt.Sprintf("%s-s3-credentials", NameCluster),
				Region:            "us-west-2",
				EndpointURL:       "https://s3.amazonaws.com",
			},
			Annotations: map[string]string{
				"testName": "scheduled-backup",
			},
			Labels: map[string]string{
				"backupWorker": "True",
			},
			PriorityClassName: PriorityClassName,
			RuntimeClassName:  RuntimeClassName,
			SchedulerName:     SchedulerName,
			VerifyTLS:         VerifyTLS,
			NodeSelector:      NodeSelector,
		},
	}
}

func toolkitDefaults(spec *apiv1.ToolkitSpec) {
	spec.Image = ImageToolkit
	spec.Resources = resources("150M", "100m", "256M", "400m")
	spec.Env = envList("TOOLKIT_ENV", "VALUE")
	spec.EnvFrom = envFromList("toolkit-env-secret")
}

func podSpecDefaults(spec *apiv1.PodSpec, image string, resources corev1.ResourceRequirements, configuration string, gracePeriod int64, env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
	spec.Image = image
	spec.Resources = resources
	spec.Configuration = configuration
	spec.TerminationGracePeriodSeconds = ptr.To(gracePeriod)
	spec.Env = env
	spec.EnvFrom = envFrom

	spec.Size = 3

	spec.RuntimeClassName = RuntimeClassName
	spec.Labels = Labels
	spec.Annotations = Annotations
	spec.PriorityClassName = PriorityClassName
	spec.SchedulerName = SchedulerName
	spec.NodeSelector = NodeSelector

	spec.ServiceAccountName = "some-service-account"
	spec.PodSecurityContext = nil
}
