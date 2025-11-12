package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/internal/marshal"
	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/pkg/defaults"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if len(os.Args) != 2 {
		fmt.Println("Expected arguments are: `ps`, `ps-backup` or `ps-restore`")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "ps":
		if err := printCluster(ctx); err != nil {
			panic(err)
		}
	case "ps-backup":
		if err := printBackup(); err != nil {
			panic(err)
		}
	case "ps-restore":
		if err := printRestore(); err != nil {
			panic(err)
		}
	default:
		fmt.Println("Unexpected argument. Expected arguments are: `ps`, `ps-backup` or `ps-restore`")
		os.Exit(1)
	}
}

func printCluster(ctx context.Context) error {
	cr := &apiv1.PerconaServerMySQL{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PerconaServerMySQL",
			APIVersion: "ps.percona.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: defaults.NameCluster,
			Finalizers: []string{
				"percona.com/delete-mysql-pods-in-order",
				"percona.com/delete-ssl",
				"percona.com/delete-mysql-pvc",
			},
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Backup: &apiv1.BackupSpec{
				Image: defaults.ImageBackup,
			},
			MySQL: apiv1.MySQLSpec{
				VolumeSpec: &apiv1.VolumeSpec{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			},
		},
		Status: apiv1.PerconaServerMySQLStatus{},
	}

	if err := cr.CheckNSetDefaults(ctx, nil); err != nil {
		return errors.Wrap(err, "check and set defaults")
	}
	defaults.ManualCluster(cr)
	if err := defaults.FromPresets(cr); err != nil {
		return errors.Wrap(err, "preset defaults")
	}

	jsonBytes, err := marshal.MarshalIgnoreOmitEmpty(cr,
		metav1.ObjectMeta{},
		corev1.ResourceRequirements{},
		corev1.SecurityContext{},
		corev1.Probe{},
		corev1.Affinity{},
		corev1.ServicePort{},
		corev1.TopologySpreadConstraint{},
		corev1.Toleration{},
		corev1.EnvVar{},
		corev1.EnvFromSource{},
		corev1.PodSecurityContext{},
		intstr.IntOrString{},
		corev1.EmptyDirVolumeSource{},
		corev1.HostPathVolumeSource{},
		corev1.PersistentVolumeClaimSpec{},
		corev1.Volume{},
		corev1.Container{},
	)
	if err != nil {
		return errors.Wrap(err, "marshal")
	}
	b, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return errors.Wrap(err, "json to yaml")
	}

	_, err = os.Stdout.Write(b)
	return errors.Wrap(err, "write")
}

func printBackup() error {
	cr := &apiv1.PerconaServerMySQLBackup{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PerconaServerMySQLBackup",
			APIVersion: "ps.percona.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: defaults.NameBackup,
			Finalizers: []string{
				"percona.com/delete-backup",
			},
		},
		Spec: apiv1.PerconaServerMySQLBackupSpec{
			ClusterName: defaults.NameCluster,
			StorageName: "minio",
			SourcePod:   defaults.SourcePod,
		},
	}

	if err := defaults.FromPresets(cr); err != nil {
		return errors.Wrap(err, "preset defaults")
	}
	jsonBytes, err := marshal.MarshalIgnoreOmitEmpty(cr,
		corev1.EnvVar{},
		corev1.EnvFromSource{},
		metav1.ObjectMeta{})
	if err != nil {
		return errors.Wrap(err, "marshal")
	}
	b, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return errors.Wrap(err, "json to yaml")
	}

	_, err = os.Stdout.Write(b)
	return errors.Wrap(err, "write")
}

func printRestore() error {
	cr := &apiv1.PerconaServerMySQLRestore{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PerconaServerMySQLRestore",
			APIVersion: "ps.percona.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: defaults.NameRestore,
		},
		Spec: apiv1.PerconaServerMySQLRestoreSpec{
			ClusterName: defaults.NameCluster,
			BackupName:  defaults.NameBackup,
			BackupSource: &apiv1.PerconaServerMySQLBackupStatus{
				Destination: "s3://S3-BACKUP-BUCKET-NAME-HERE/backup-path",
				Storage: &apiv1.BackupStorageSpec{
					Type: apiv1.BackupStorageS3,
					S3: &apiv1.BackupStorageS3Spec{
						Bucket:            "S3-BACKUP-BUCKET-NAME-HERE",
						Prefix:            "PREFIX_NAME",
						CredentialsSecret: fmt.Sprintf("%s-s3-credentials", defaults.NameCluster),
						Region:            "us-west-2",
						EndpointURL:       "https://s3.amazonaws.com",
					},
					Labels:            defaults.Labels,
					Annotations:       defaults.Annotations,
					SchedulerName:     defaults.SchedulerName,
					PriorityClassName: defaults.PriorityClassName,
					NodeSelector:      defaults.NodeSelector,
					VerifyTLS:         defaults.VerifyTLS,
					RuntimeClassName:  defaults.RuntimeClassName,
				},
			},
		},
	}

	if err := defaults.FromPresets(cr); err != nil {
		return errors.Wrap(err, "preset defaults")
	}
	jsonBytes, err := marshal.MarshalIgnoreOmitEmpty(cr,
		corev1.EnvVar{},
		corev1.EnvFromSource{},
		metav1.ObjectMeta{},
		corev1.Affinity{},
		corev1.PodSecurityContext{},
		corev1.SecurityContext{},
		corev1.TopologySpreadConstraint{},
		corev1.Volume{},
		corev1.PersistentVolumeClaimSpec{},
		corev1.EmptyDirVolumeSource{},
		corev1.HostPathVolumeSource{},
		corev1.ResourceRequirements{},
		corev1.Toleration{},
	)
	if err != nil {
		return errors.Wrap(err, "marshal")
	}
	b, err := yaml.JSONToYAML(jsonBytes)
	if err != nil {
		return errors.Wrap(err, "json to yaml")
	}

	_, err = os.Stdout.Write(b)
	return errors.Wrap(err, "write")
}
