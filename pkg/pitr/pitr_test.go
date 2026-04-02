package pitr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func TestRestoreJob(t *testing.T) {
	tests := map[string]struct {
		cluster   *apiv1.PerconaServerMySQL
		restore   *apiv1.PerconaServerMySQLRestore
		storage   *apiv1.BackupStorageSpec
		initImage string
		verify    func(t *testing.T, job *batchv1.Job)
	}{
		"basic job metadata": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-cluster",
					Namespace: "test-ns",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "my-cluster-secrets",
					SSLSecretName: "my-cluster-ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "my-restore",
					Namespace: "test-ns",
				},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Equal(t, "pitr-restore-my-restore", job.Name)
				assert.Equal(t, "test-ns", job.Namespace)
				assert.Equal(t, "batch/v1", job.APIVersion)
				assert.Equal(t, "Job", job.Kind)
			},
		},
		"job spec parallelism and completions": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "cluster-secrets",
					SSLSecretName: "cluster-ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Equal(t, ptr.To(int32(1)), job.Spec.Parallelism)
				assert.Equal(t, ptr.To(int32(1)), job.Spec.Completions)
				assert.Equal(t, corev1.RestartPolicyNever, job.Spec.Template.Spec.RestartPolicy)
			},
		},
		"backoff limit from cluster spec": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "cluster-secrets",
					SSLSecretName: "cluster-ssl",
					Backup: &apiv1.BackupSpec{
						BackoffLimit: ptr.To(int32(5)),
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Equal(t, ptr.To(int32(5)), job.Spec.BackoffLimit)
			},
		},
		"pvc name uses cluster name": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "mycluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "mycluster-secrets",
					SSLSecretName: "mycluster-ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				volumes := job.Spec.Template.Spec.Volumes
				expectedPVCName := mysql.DataVolumeName + "-mycluster-mysql-0"
				var found bool
				for _, v := range volumes {
					if v.Name == dataVolumeName && v.PersistentVolumeClaim != nil {
						assert.Equal(t, expectedPVCName, v.PersistentVolumeClaim.ClaimName)
						found = true
					}
				}
				assert.True(t, found, "datadir volume with expected PVC not found")
			},
		},
		"volumes include all expected volumes": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "my-secrets",
					SSLSecretName: "my-ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				volumeNames := map[string]bool{}
				for _, v := range job.Spec.Template.Spec.Volumes {
					volumeNames[v.Name] = true
				}
				assert.True(t, volumeNames[apiv1.BinVolumeName], "missing bin volume")
				assert.True(t, volumeNames[dataVolumeName], "missing datadir volume")
				assert.True(t, volumeNames[credsVolumeName], "missing creds volume")
				assert.True(t, volumeNames[tlsVolumeName], "missing tls volume")
				assert.True(t, volumeNames[binlogsVolumeName], "missing binlogs volume")
			},
		},
		"secrets volume uses cluster secrets name": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "custom-secrets",
					SSLSecretName: "custom-ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				for _, v := range job.Spec.Template.Spec.Volumes {
					switch v.Name {
					case credsVolumeName:
						assert.Equal(t, "custom-secrets", v.Secret.SecretName)
					case tlsVolumeName:
						assert.Equal(t, "custom-ssl", v.Secret.SecretName)
					}
				}
			},
		},
		"binlogs configmap volume references restore name": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				for _, v := range job.Spec.Template.Spec.Volumes {
					if v.Name == binlogsVolumeName {
						assert.Equal(t, "pitr-binlogs-my-restore", v.ConfigMap.Name)
						return
					}
				}
				t.Error("binlogs volume not found")
			},
		},
		"storage scheduling fields propagated to pod spec": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage: &apiv1.BackupStorageSpec{
				NodeSelector:      map[string]string{"disktype": "ssd"},
				SchedulerName:     "my-scheduler",
				PriorityClassName: "high-priority",
				Tolerations: []corev1.Toleration{
					{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "mysql", Effect: corev1.TaintEffectNoSchedule},
				},
			},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				spec := job.Spec.Template.Spec
				assert.Equal(t, map[string]string{"disktype": "ssd"}, spec.NodeSelector)
				assert.Equal(t, "my-scheduler", spec.SchedulerName)
				assert.Equal(t, "high-priority", spec.PriorityClassName)
				assert.Len(t, spec.Tolerations, 1)
				assert.Equal(t, "dedicated", spec.Tolerations[0].Key)
			},
		},
		"image pull secrets from backup spec": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-secret"}},
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Equal(t, []corev1.LocalObjectReference{{Name: "registry-secret"}}, job.Spec.Template.Spec.ImagePullSecrets)
			},
		},
		"restore container has correct env vars without pitr spec": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore", Namespace: "ns"},
				Spec:       apiv1.PerconaServerMySQLRestoreSpec{},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				container := job.Spec.Template.Spec.Containers[0]
				envMap := envToMap(container.Env)
				assert.Equal(t, "my-restore", envMap["RESTORE_NAME"])
				assert.Equal(t, binlogsMountPath+"/"+BinlogsConfigKey, envMap["BINLOGS_PATH"])
				assert.NotContains(t, envMap, "PITR_TYPE")
				assert.NotContains(t, envMap, "PITR_DATE")
				assert.NotContains(t, envMap, "PITR_GTID")
			},
		},
		"restore container has pitr date env vars": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLRestoreSpec{
					PITR: &apiv1.RestorePITRSpec{
						Type: apiv1.PITRDate,
						Date: "2024-01-15 10:00:00",
					},
				},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				container := job.Spec.Template.Spec.Containers[0]
				envMap := envToMap(container.Env)
				assert.Equal(t, "date", envMap["PITR_TYPE"])
				assert.Equal(t, "2024-01-15 10:00:00", envMap["PITR_DATE"])
				assert.NotContains(t, envMap, "PITR_GTID")
			},
		},
		"restore container has pitr gtid env vars": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLRestoreSpec{
					PITR: &apiv1.RestorePITRSpec{
						Type: apiv1.PITRGtid,
						GTID: "abc123:1-100",
					},
				},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				container := job.Spec.Template.Spec.Containers[0]
				envMap := envToMap(container.Env)
				assert.Equal(t, "gtid", envMap["PITR_TYPE"])
				assert.Equal(t, "abc123:1-100", envMap["PITR_GTID"])
				assert.NotContains(t, envMap, "PITR_DATE")
			},
		},
		"restore container has s3 env vars when binlog server has s3 storage": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{
								Storage: apiv1.BinlogServerStorageSpec{
									S3: &apiv1.BackupStorageS3Spec{
										Bucket:            "my-bucket",
										CredentialsSecret: "s3-creds",
										Region:            "us-east-1",
										EndpointURL:       "https://s3.example.com",
									},
								},
							},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				container := job.Spec.Template.Spec.Containers[0]
				envMap := envToMap(container.Env)
				assert.Equal(t, "s3", envMap["STORAGE_TYPE"])
				assert.Equal(t, "my-bucket", envMap["S3_BUCKET"])
				assert.Equal(t, "us-east-1", envMap["AWS_DEFAULT_REGION"])
				assert.Equal(t, "https://s3.example.com", envMap["AWS_ENDPOINT"])

				envByName := envByNameMap(container.Env)
				accessKey := envByName["AWS_ACCESS_KEY_ID"]
				assert.NotNil(t, accessKey.ValueFrom)
				assert.Equal(t, "s3-creds", accessKey.ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "AWS_ACCESS_KEY_ID", accessKey.ValueFrom.SecretKeyRef.Key)

				secretKey := envByName["AWS_SECRET_ACCESS_KEY"]
				assert.NotNil(t, secretKey.ValueFrom)
				assert.Equal(t, "s3-creds", secretKey.ValueFrom.SecretKeyRef.Name)
				assert.Equal(t, "AWS_SECRET_ACCESS_KEY", secretKey.ValueFrom.SecretKeyRef.Key)
			},
		},
		"restore container command and volume mounts": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
					MySQL: apiv1.MySQLSpec{},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "init:latest",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Len(t, job.Spec.Template.Spec.Containers, 1)
				container := job.Spec.Template.Spec.Containers[0]
				assert.Equal(t, appName, container.Name)
				assert.Equal(t, []string{"/opt/percona/run-pitr-restore.sh"}, container.Command)

				mountNames := map[string]bool{}
				for _, m := range container.VolumeMounts {
					mountNames[m.Name] = true
				}
				assert.True(t, mountNames[apiv1.BinVolumeName])
				assert.True(t, mountNames[dataVolumeName])
				assert.True(t, mountNames[credsVolumeName])
				assert.True(t, mountNames[tlsVolumeName])
				assert.True(t, mountNames[binlogsVolumeName])
			},
		},
		"one init container present": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					SecretsName:   "secrets",
					SSLSecretName: "ssl",
					Backup: &apiv1.BackupSpec{
						PiTR: apiv1.PiTRSpec{
							BinlogServer: &apiv1.BinlogServerSpec{},
						},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore", Namespace: "ns"},
			},
			storage:   &apiv1.BackupStorageSpec{},
			initImage: "percona/init:1.0",
			verify: func(t *testing.T, job *batchv1.Job) {
				assert.Len(t, job.Spec.Template.Spec.InitContainers, 1)
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			job := RestoreJob(tt.cluster, tt.restore, tt.storage, tt.initImage)
			tt.verify(t, job)
		})
	}
}

func envToMap(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		m[e.Name] = e.Value
	}
	return m
}

func envByNameMap(envs []corev1.EnvVar) map[string]corev1.EnvVar {
	m := make(map[string]corev1.EnvVar, len(envs))
	for _, e := range envs {
		m[e.Name] = e
	}
	return m
}

func TestJobName(t *testing.T) {
	tests := map[string]struct {
		restoreName string
		expected    string
	}{
		"simple name":         {restoreName: "my-restore", expected: "pitr-restore-my-restore"},
		"name with numbers":   {restoreName: "restore-123", expected: "pitr-restore-restore-123"},
		"single word name":    {restoreName: "restore", expected: "pitr-restore-restore"},
		"name with dots":      {restoreName: "restore.v1", expected: "pitr-restore-restore.v1"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			restore := &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: tt.restoreName},
			}
			assert.Equal(t, tt.expected, JobName(restore))
		})
	}
}

func TestBinlogsConfigMap(t *testing.T) {
	tests := map[string]struct {
		cluster  *apiv1.PerconaServerMySQL
		restore  *apiv1.PerconaServerMySQLRestore
		verify   func(t *testing.T, cm *corev1.ConfigMap)
	}{
		"basic metadata": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "test-ns"},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "my-restore"},
			},
			verify: func(t *testing.T, cm *corev1.ConfigMap) {
				assert.Equal(t, "pitr-binlogs-my-restore", cm.Name)
				assert.Equal(t, "test-ns", cm.Namespace)
				assert.Equal(t, "v1", cm.TypeMeta.APIVersion)
				assert.Equal(t, "ConfigMap", cm.TypeMeta.Kind)
			},
		},
		"no global labels or annotations": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore"},
			},
			verify: func(t *testing.T, cm *corev1.ConfigMap) {
				assert.Nil(t, cm.Annotations)
			},
		},
		"global labels merged into configmap labels": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					Metadata: &apiv1.Metadata{
						Labels: map[string]string{"env": "prod"},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore"},
			},
			verify: func(t *testing.T, cm *corev1.ConfigMap) {
				assert.Equal(t, "prod", cm.Labels["env"])
			},
		},
		"global annotations propagated": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
				Spec: apiv1.PerconaServerMySQLSpec{
					Metadata: &apiv1.Metadata{
						Annotations: map[string]string{"team": "dba"},
					},
				},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "restore"},
			},
			verify: func(t *testing.T, cm *corev1.ConfigMap) {
				assert.Equal(t, "dba", cm.Annotations["team"])
			},
		},
		"name derived from restore name": {
			cluster: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster", Namespace: "ns"},
			},
			restore: &apiv1.PerconaServerMySQLRestore{
				ObjectMeta: metav1.ObjectMeta{Name: "weekly-restore"},
			},
			verify: func(t *testing.T, cm *corev1.ConfigMap) {
				assert.Equal(t, "pitr-binlogs-weekly-restore", cm.Name)
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cm := BinlogsConfigMap(tt.cluster, tt.restore)
			tt.verify(t, cm)
		})
	}
}
