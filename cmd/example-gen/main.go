package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/yaml"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/defaults"
	"github.com/percona/percona-server-mysql-operator/cmd/example-gen/internal/marshal"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := printPerconaServerMySQL(ctx); err != nil {
		panic(err)
	}
}

func printPerconaServerMySQL(ctx context.Context) error {
	cr := &apiv1.PerconaServerMySQL{
		TypeMeta: metav1.TypeMeta{
			Kind:       "PerconaServerMySQL",
			APIVersion: "ps.percona.com/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "ps-cluster1",
			Finalizers: []string{
				"percona.com/delete-mysql-pods-in-order",
				"percona.com/delete-ssl",
				"percona.com/delete-mysql-pvc",
			},
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			Backup: &apiv1.BackupSpec{
				Image: "some-backup",
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
	if err := defaults.PresetCluster(cr); err != nil {
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
