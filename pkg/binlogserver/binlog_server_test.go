package binlogserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func newTestCR(name, namespace string) *apiv1.PerconaServerMySQL {
	return &apiv1.PerconaServerMySQL{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: apiv1.PerconaServerMySQLSpec{
			SSLSecretName: name + "-ssl",
			SecretsName:   name + "-secrets",
			Backup: &apiv1.BackupSpec{
				PiTR: apiv1.PiTRSpec{
					BinlogServer: &apiv1.BinlogServerSpec{
						Storage: apiv1.BinlogServerStorageSpec{
							S3: &apiv1.BackupStorageS3Spec{
								CredentialsSecret: "s3-creds-secret",
							},
						},
					},
				},
			},
		},
	}
}

func TestStatefulSet(t *testing.T) {
	tests := map[string]struct {
		cr         *apiv1.PerconaServerMySQL
		initImage  string
		configHash string
		verify     func(t *testing.T, cr *apiv1.PerconaServerMySQL)
	}{
		"object meta": {
			cr:         newTestCR("my-cluster", "test-ns"),
			initImage:  "init:latest",
			configHash: "abc123",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "abc123")

				assert.Equal(t, "apps/v1", sts.TypeMeta.APIVersion)
				assert.Equal(t, "StatefulSet", sts.TypeMeta.Kind)
				assert.Equal(t, "my-cluster-binlog-server", sts.Name)
				assert.Equal(t, "test-ns", sts.Namespace)
			},
		},
		"labels": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")

				expectedLabels := MatchLabels(cr)
				assert.Equal(t, expectedLabels, sts.Labels)
				assert.Equal(t, expectedLabels, sts.Spec.Selector.MatchLabels)
				assert.Equal(t, expectedLabels, sts.Spec.Template.Labels)
			},
		},
		"replicas is always 1": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				assert.Equal(t, ptr.To(int32(1)), sts.Spec.Replicas)
			},
		},
		"config hash annotation in pod template": {
			cr:         newTestCR("cluster", "ns"),
			initImage:  "init:latest",
			configHash: "deadbeef",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "2pacisnotdead")
				assert.Equal(t, "deadbeef", sts.Spec.Template.Annotations[string(naming.AnnotationConfigHash)])
			},
		},
		"empty config hash produces no annotation": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				assert.NotContains(t, sts.Spec.Template.Annotations, string(naming.AnnotationConfigHash))
			},
		},
		"init container uses provided image": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "percona/init:1.2.3",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "percona/init:1.2.3", "")
				assert.Len(t, sts.Spec.Template.Spec.InitContainers, 1)
				assert.Equal(t, "percona/init:1.2.3", sts.Spec.Template.Spec.InitContainers[0].Image)
			},
		},
		"binlog server container present with correct name": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				assert.Len(t, sts.Spec.Template.Spec.Containers, 1)
				assert.Equal(t, AppName, sts.Spec.Template.Spec.Containers[0].Name)
			},
		},
		"container command and args": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				container := sts.Spec.Template.Spec.Containers[0]
				assert.Equal(t, []string{"/opt/percona/binlog-server-entrypoint.sh"}, container.Command)
				assert.Equal(t, []string{
					binlogServerBinary,
					"pull",
					configMountPath + "/" + ConfigKey,
				}, container.Args)
			},
		},
		"binlog server container image from spec": {
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newTestCR("cluster", "ns")
				cr.Spec.Backup.PiTR.BinlogServer.Image = "percona/binlog-server:2.0"
				return cr
			}(),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				container := sts.Spec.Template.Spec.Containers[0]
				assert.Equal(t, "percona/binlog-server:2.0", container.Image)
			},
		},
		"volumes include all expected volumes": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				volumeNames := map[string]bool{}
				for _, v := range sts.Spec.Template.Spec.Volumes {
					volumeNames[v.Name] = true
				}
				assert.True(t, volumeNames[apiv1.BinVolumeName], "missing bin volume")
				assert.True(t, volumeNames[bufferVolumeName], "missing buffer volume")
				assert.True(t, volumeNames[credsVolumeName], "missing creds volume")
				assert.True(t, volumeNames[tlsVolumeName], "missing tls volume")
				assert.True(t, volumeNames[storageCredsVolumeName], "missing storage volume")
				assert.True(t, volumeNames[configVolumeName], "missing config volume")
			},
		},
		"creds volume uses internal secret name": {
			cr:        newTestCR("mycluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				for _, v := range sts.Spec.Template.Spec.Volumes {
					if v.Name == credsVolumeName {
						assert.Equal(t, cr.InternalSecretName(), v.Secret.SecretName)
						return
					}
				}
				t.Error("creds volume not found")
			},
		},
		"tls volume uses ssl secret name": {
			cr:        newTestCR("mycluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				for _, v := range sts.Spec.Template.Spec.Volumes {
					if v.Name == tlsVolumeName {
						assert.Equal(t, "mycluster-ssl", v.Secret.SecretName)
						return
					}
				}
				t.Error("tls volume not found")
			},
		},
		"storage volume uses s3 credentials secret": {
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newTestCR("cluster", "ns")
				cr.Spec.Backup.PiTR.BinlogServer.Storage.S3.CredentialsSecret = "my-s3-creds"
				return cr
			}(),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				for _, v := range sts.Spec.Template.Spec.Volumes {
					if v.Name == storageCredsVolumeName {
						assert.Equal(t, "my-s3-creds", v.Secret.SecretName)
						return
					}
				}
				t.Error("storage volume not found")
			},
		},
		"config volume references config secret": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				for _, v := range sts.Spec.Template.Spec.Volumes {
					if v.Name == configVolumeName {
						assert.NotNil(t, v.Projected)
						var hasConfigSecret bool
						for _, src := range v.Projected.Sources {
							if src.Secret != nil && src.Secret.Name == ConfigSecretName(cr) {
								hasConfigSecret = true
							}
						}
						assert.True(t, hasConfigSecret, "config volume should reference config secret")
						return
					}
				}
				t.Error("config volume not found")
			},
		},
		"global annotations propagated to statefulset": {
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newTestCR("cluster", "ns")
				cr.Spec.Metadata = &apiv1.Metadata{
					Annotations: map[string]string{"team": "dba"},
				}
				return cr
			}(),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				assert.Equal(t, "dba", sts.Annotations["team"])
			},
		},
		"global labels propagated to statefulset": {
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newTestCR("cluster", "ns")
				cr.Spec.Metadata = &apiv1.Metadata{
					Labels: map[string]string{"env": "prod"},
				}
				return cr
			}(),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				assert.Equal(t, "prod", sts.Labels["env"])
			},
		},
		"container volume mounts include all expected mounts": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				container := sts.Spec.Template.Spec.Containers[0]
				mountNames := map[string]bool{}
				for _, m := range container.VolumeMounts {
					mountNames[m.Name] = true
				}
				assert.True(t, mountNames[apiv1.BinVolumeName], "missing bin volume mount")
				assert.True(t, mountNames[credsVolumeName], "missing creds volume mount")
				assert.True(t, mountNames[tlsVolumeName], "missing tls volume mount")
				assert.True(t, mountNames[configVolumeName], "missing config volume mount")
				assert.True(t, mountNames[bufferVolumeName], "missing buffer volume mount")
			},
		},
		"container env includes CONFIG_PATH and CUSTOM_CONFIG_PATH": {
			cr:        newTestCR("cluster", "ns"),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				container := sts.Spec.Template.Spec.Containers[0]
				envMap := make(map[string]string)
				for _, e := range container.Env {
					envMap[e.Name] = e.Value
				}
				assert.Contains(t, envMap, "CONFIG_PATH")
				assert.Contains(t, envMap, "CUSTOM_CONFIG_PATH")
			},
		},
		"custom env vars from spec are appended": {
			cr: func() *apiv1.PerconaServerMySQL {
				cr := newTestCR("cluster", "ns")
				cr.Spec.Backup.PiTR.BinlogServer.Env = []corev1.EnvVar{
					{Name: "MY_CUSTOM_VAR", Value: "custom-value"},
				}
				return cr
			}(),
			initImage: "init:latest",
			verify: func(t *testing.T, cr *apiv1.PerconaServerMySQL) {
				sts := StatefulSet(cr, "init:latest", "")
				container := sts.Spec.Template.Spec.Containers[0]
				envMap := make(map[string]string)
				for _, e := range container.Env {
					envMap[e.Name] = e.Value
				}
				assert.Equal(t, "custom-value", envMap["MY_CUSTOM_VAR"])
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			tt.verify(t, tt.cr)
		})
	}
}
