package binlogserver

import (
	"path"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const (
	AppName                 = "binlog-server"
	credsVolumeName         = "users"
	CredsMountPath          = "/etc/mysql/mysql-users-secret"
	tlsVolumeName           = "tls"
	TLSMountPath            = "/etc/mysql/mysql-tls-secret"
	bufferVolumeName        = "buffer"
	BufferMountPath         = "/var/lib/binlogsrv"
	ConfigVolumeName        = "config"
	ConfigMountPath         = "/etc/binlog_server/config"
	storageCredsVolumeName  = "storage"
	ConfigKey               = "config.json"
	customConfigKey         = "custom.json"
	keyringMountPath        = "/etc/binlog_server/keyring"
	keyringVolumeName       = "keyring"
	searchBufferVolumeName  = "binlog-server-buffer"
	searchCredsVolumeName   = "binlog-server-users"
	searchKeyringVolumeName = "binlog-server-keyring"
)

func Name(cr *apiv1.PerconaServerMySQL) string {
	return cr.Name + "-" + AppName
}

func RestoreSpec(cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) *apiv1.BinlogServerSpec {
	clusterSpec := cr.Spec.Backup.PiTR.BinlogServer
	spec := clusterSpec
	if restore.Spec.PITR != nil && restore.Spec.PITR.BackupSource != nil && restore.Spec.PITR.BackupSource.BinlogServer != nil {
		spec = restore.Spec.PITR.BackupSource.BinlogServer
	}
	if spec == nil {
		return nil
	}

	spec = spec.DeepCopy()
	if spec.Image == "" && clusterSpec != nil {
		spec.Image = clusterSpec.Image
	}
	spec.SetDefaults()
	return spec
}

func RestoreName(cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) string {
	const controllerRevisionHashLength = 11
	maxRestoreStatefulSetNameLength := validation.DNS1123LabelMaxLength - controllerRevisionHashLength
	name := Name(cr) + "-r-" + restore.Name
	return naming.TruncateNameWithHash(name, maxRestoreStatefulSetNameLength, "-")
}

func customConfigMapName(cr *apiv1.PerconaServerMySQL) string {
	return cr.Name + "-" + AppName + "-config"
}

func ConfigSecretName(cr *apiv1.PerconaServerMySQL) string {
	return cr.Name + "-" + AppName + "-config"
}

func RestoreConfigSecretName(cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) string {
	name := cr.Name + "-" + AppName + "-config-restore-" + restore.Name
	return naming.TruncateNameWithHash(name, validation.DNS1123SubdomainMaxLength, "-r-")
}

func MatchLabels(cr *apiv1.PerconaServerMySQL) map[string]string {
	return util.SSMapMerge(
		cr.GlobalLabels(),
		cr.MySQLSpec().Labels,
		cr.Labels(AppName, naming.ComponentPITR),
	)
}

func RestoreMatchLabels(cr *apiv1.PerconaServerMySQL, restore *apiv1.PerconaServerMySQLRestore) map[string]string {
	return util.SSMapMerge(
		cr.GlobalLabels(),
		restore.Labels(AppName, naming.ComponentPITR),
	)
}

func StatefulSet(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec, labels map[string]string, initImage, configHash, configSecretName string) *appsv1.StatefulSet {
	if configSecretName == "" {
		configSecretName = ConfigSecretName(cr)
	}

	annotations := make(map[string]string)
	if configHash != "" {
		annotations[string(naming.AnnotationConfigHash)] = configHash
	}

	podContainers := containers(cr, spec)
	initContainers := []corev1.Container{
		k8s.InitContainer(
			cr,
			AppName,
			initImage,
			nil,
			spec.ImagePullPolicy,
			spec.ContainerSecurityContext,
			spec.Resources,
			nil,
		),
	}
	if s := spec.Storage.S3; s != nil && s.CABundle != nil &&
		cr.CompareVersion("1.3.0") >= 0 {
		initContainers[0].VolumeMounts = append(initContainers[0].VolumeMounts,
			corev1.VolumeMount{Name: naming.S3CertsInputVolumeName, MountPath: naming.S3CertsInputMountPath, ReadOnly: true},
			corev1.VolumeMount{Name: naming.S3CertsVolumeName, MountPath: naming.S3CertsMountPath},
		)
	}

	return &appsv1.StatefulSet{
		APIVersion:  "apps/v1",
		Kind:        "StatefulSet",
		Name:        Name(cr),
		Namespace:   cr.Namespace,
		Labels:      labels,
		Annotations: cr.GlobalAnnotations(),
		Spec: appsv1.StatefulSetSpec{
			Replicas: new(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: util.SSMapMerge(cr.GlobalAnnotations(), annotations),
				},
				Spec: spec.Core(
					labels,
					volumes(cr, spec, configSecretName),
					initContainers,
					podContainers,
				),
			},
		},
	}
}

func sslDisabled(spec *apiv1.BinlogServerSpec) bool {
	return spec.SSLMode == "disabled"
}

func volumes(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec, configSecretName string) []corev1.Volume {
	vols := []corev1.Volume{
		{
			Name:     apiv1.BinVolumeName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
		{
			Name:     bufferVolumeName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
		{
			Name: credsVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: cr.InternalSecretName(),
			},
		},
	}

	if !sslDisabled(spec) {
		vols = append(vols, corev1.Volume{
			Name: tlsVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: cr.Spec.SSLSecretName,
			},
		})
	}

	vols = append(
		vols,
		corev1.Volume{
			Name: storageCredsVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: spec.Storage.S3.CredentialsSecret,
			},
		},
		ConfigVolume(cr, spec, configSecretName),
	)

	if s := spec.Storage.S3; s != nil && s.CABundle != nil &&
		cr.CompareVersion("1.3.0") >= 0 {
		selectors := []apiv1.CABundleSecretSelector{*s.CABundle}
		vols = append(vols, k8s.S3CertVolumes(selectors)...)
		vols = append(vols, corev1.Volume{
			Name:     naming.S3CertsVolumeName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		})
	}

	if spec.KeyringSecret != nil {
		vols = append(vols, corev1.Volume{
			Name: keyringVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: spec.KeyringSecret.Name,
			},
		})
	}
	return vols
}

func ConfigVolume(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec, configSecretName string) corev1.Volume {
	t := true

	if configSecretName == "" {
		configSecretName = ConfigSecretName(cr)
	}

	conf := Configurable{cr: cr, spec: spec}

	return corev1.Volume{
		Name: ConfigVolumeName,
		Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{
				{
					Secret: &corev1.SecretProjection{
						Name: configSecretName,
						Items: []corev1.KeyToPath{
							{
								Key:  ConfigKey,
								Path: ConfigKey,
							},
						},
					},
				},
				{
					ConfigMap: &corev1.ConfigMapProjection{
						Name: conf.GetConfigMapName(),
						Items: []corev1.KeyToPath{
							{
								Key:  conf.GetConfigMapKey(),
								Path: conf.GetConfigMapKey(),
							},
						},
						Optional: &t,
					},
				},
			},
		},
	}
}

func SearchVolumes(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec, configSecretName string) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name:     searchBufferVolumeName,
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
		{
			Name: searchCredsVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: cr.InternalSecretName(),
			},
		},
		ConfigVolume(cr, spec, configSecretName),
	}
	if spec.KeyringSecret != nil {
		volumes = append(volumes, corev1.Volume{
			Name: searchKeyringVolumeName,
			Secret: &corev1.SecretVolumeSource{
				SecretName: spec.KeyringSecret.Name,
			},
		})
	}
	return volumes
}

func SearchContainer(spec *apiv1.BinlogServerSpec, subcommand, arg, outputVolumeName, outputMountPath, outputFile string) corev1.Container {
	configPath := path.Join(ConfigMountPath, ConfigKey)
	outputPath := path.Join(outputMountPath, outputFile)
	searchScript := `output_path=$1
shift
if ! "$@" > "$output_path"; then
	cat "$output_path" >&2
	exit 1
fi
cat "$output_path"`

	container := corev1.Container{
		Name:            SearchContainerName,
		Image:           spec.Image,
		ImagePullPolicy: spec.ImagePullPolicy,
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{searchScript, "binlog-search", outputPath, BinlogServerBinary, subcommand, configPath, arg},
		Env:             spec.Env,
		EnvFrom:         spec.EnvFrom,
		VolumeMounts: []corev1.VolumeMount{
			{Name: outputVolumeName, MountPath: outputMountPath},
			{Name: ConfigVolumeName, MountPath: ConfigMountPath},
			{Name: searchBufferVolumeName, MountPath: BufferMountPath},
			{Name: searchCredsVolumeName, MountPath: CredsMountPath},
			{Name: tlsVolumeName, MountPath: TLSMountPath},
		},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
		Resources:                spec.Resources,
	}
	if spec.KeyringSecret != nil {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name: searchKeyringVolumeName, MountPath: keyringMountPath,
		})
	}
	return container
}

func containers(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec) []corev1.Container {
	return []corev1.Container{binlogServerContainer(cr, spec)}
}

func binlogServerContainer(cr *apiv1.PerconaServerMySQL, spec *apiv1.BinlogServerSpec) corev1.Container {
	mounts := []corev1.VolumeMount{
		{
			Name:      apiv1.BinVolumeName,
			MountPath: apiv1.BinVolumePath,
		},
		{
			Name:      credsVolumeName,
			MountPath: CredsMountPath,
		},
	}
	if !sslDisabled(spec) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      tlsVolumeName,
			MountPath: TLSMountPath,
		})
	}
	mounts = append(
		mounts,
		corev1.VolumeMount{
			Name:      ConfigVolumeName,
			MountPath: ConfigMountPath,
		},
		corev1.VolumeMount{
			Name:      bufferVolumeName,
			MountPath: BufferMountPath,
		},
	)

	if spec.KeyringSecret != nil {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      keyringVolumeName,
			MountPath: keyringMountPath,
		})
	}

	container := corev1.Container{
		Name:                     AppName,
		Image:                    spec.Image,
		ImagePullPolicy:          spec.ImagePullPolicy,
		Resources:                spec.Resources,
		Env:                      spec.Env,
		EnvFrom:                  spec.EnvFrom,
		VolumeMounts:             mounts,
		Command:                  []string{"/opt/percona/binlog-server-entrypoint.sh"},
		Args:                     []string{BinlogServerBinary, "pull", path.Join(ConfigMountPath, ConfigKey)},
		TerminationMessagePath:   "/dev/termination-log",
		TerminationMessagePolicy: corev1.TerminationMessageReadFile,
		SecurityContext:          spec.ContainerSecurityContext,
	}
	if s := spec.Storage.S3; s != nil && s.CABundle != nil && cr.CompareVersion("1.3.0") >= 0 {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      naming.S3CertsVolumeName,
			MountPath: naming.SystemCABundlePath,
			SubPath:   "ca-bundle.crt",
			ReadOnly:  true,
		})
	}
	return container
}
