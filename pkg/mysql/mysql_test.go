package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestStatefulSet(t *testing.T) {
	configHash := "123abc"
	tlsHash := "123abc"
	initImage := "percona/init:latest"

	expectedAnnotations := map[string]string{
		string(naming.AnnotationTLSHash):    tlsHash,
		string(naming.AnnotationConfigHash): configHash,
	}

	expectedPVCs := []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "datadir",
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						"storage": resource.MustParse("1Gi"),
					},
				},
			},
		},
	}

	tests := map[string]struct {
		mysqlSpec           apiv1alpha1.MySQLSpec
		expectedStatefulSet appsv1.StatefulSet
	}{
		"pvc configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: map[corev1.ResourceName]resource.Quantity{
									corev1.ResourceStorage: resource.MustParse("1Gi"),
								},
							},
						},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes:                       expectedVolumes(),
						},
					},
					VolumeClaimTemplates: expectedPVCs,
					ServiceName:          "cluster1-mysql",
				},
			},
		},
		"host path configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						HostPath: &corev1.HostPathVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "cluster1-mysql",
				},
			},
		},
		"empty dir configured": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										EmptyDir: &corev1.EmptyDirVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "cluster1-mysql",
				},
			},
		},
		"both entry dir and host path provided - only host path is actually configured due to higher priority": {
			mysqlSpec: apiv1alpha1.MySQLSpec{
				PodSpec: apiv1alpha1.PodSpec{
					Size:                          3,
					TerminationGracePeriodSeconds: ptr.To(int64(30)),
					VolumeSpec: &apiv1alpha1.VolumeSpec{
						EmptyDir: &corev1.EmptyDirVolumeSource{},
						HostPath: &corev1.HostPathVolumeSource{},
					},
				},
			},
			expectedStatefulSet: appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1-mysql",
					Namespace: "test-ns",
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: expectedAnnotations,
						},
						Spec: corev1.PodSpec{
							InitContainers: []corev1.Container{
								{
									Image: initImage,
								},
							},
							TerminationGracePeriodSeconds: ptr.To(int64(30)),
							Volumes: append(expectedVolumes(),
								corev1.Volume{
									Name: "datadir",
									VolumeSource: corev1.VolumeSource{
										HostPath: &corev1.HostPathVolumeSource{},
									},
								},
							),
						},
					},
					ServiceName: "cluster1-mysql",
				},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cr := &apiv1alpha1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1",
					Namespace: "test-ns",
				},
				Spec: apiv1alpha1.PerconaServerMySQLSpec{
					MySQL: tt.mysqlSpec,
				},
			}

			sts := StatefulSet(cr, initImage, configHash, tlsHash, nil)

			assert.NotNil(t, sts)
			assert.Equal(t, tt.expectedStatefulSet.Name, sts.Name)
			assert.Equal(t, tt.expectedStatefulSet.Namespace, sts.Namespace)
			assert.Equal(t, tt.expectedStatefulSet.Spec.Replicas, sts.Spec.Replicas)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Annotations, sts.Spec.Template.Annotations)

			assert.Equal(t, tt.expectedStatefulSet.Spec.ServiceName, sts.Spec.ServiceName)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Spec.Volumes, sts.Spec.Template.Spec.Volumes)

			initContainers := sts.Spec.Template.Spec.InitContainers
			assert.Len(t, initContainers, 1)
			assert.Equal(t, initImage, initContainers[0].Image)

			assert.Equal(t, tt.expectedStatefulSet.Spec.Template.Spec.TerminationGracePeriodSeconds, sts.Spec.Template.Spec.TerminationGracePeriodSeconds)

			assert.Equal(t, tt.expectedStatefulSet.Spec.VolumeClaimTemplates, sts.Spec.VolumeClaimTemplates)
		})
	}
}

func expectedVolumes() []corev1.Volume {
	return []corev1.Volume{
		{
			Name: "bin",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "mysqlsh",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "users",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: "internal-cluster1",
				},
			},
		},
		{
			Name: "tls",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: "",
				},
			},
		},
		{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "my-config.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
						{
							ConfigMap: &corev1.ConfigMapProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "auto-cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "auto-config.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "cluster1-mysql",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "my.cnf",
										Path: "my-secret.cnf",
									},
								},
								Optional: ptr.To(true),
							},
						},
					},
				},
			},
		},
		{
			Name: "backup-logs",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}
}
