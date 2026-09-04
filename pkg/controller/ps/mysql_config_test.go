package ps

import (
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	clientcmdmock "github.com/percona/percona-server-mysql-operator/pkg/clientcmd/mock"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func TestReconcileMySQLConfig(t *testing.T) {
	const (
		crName           = "cluster1"
		ns               = "config-ns"
		configuratorPass = "configurator-password"
		staleHash        = "stale-config-hash"
		podCount         = 3
		stderrReadOnly   = "ERROR 1238 (HY000): Variable 'innodb_buffer_pool_size' is a read only variable\n"
		stderrDenied     = "ERROR 1227 (42000): Access denied\n"
		stderrUnknown    = "ERROR 1193 (HY000): Unknown system variable\n"
	)

	newCR := func(crVersion string, state apiv1.StatefulAppState) *apiv1.PerconaServerMySQL {
		return &apiv1.PerconaServerMySQL{
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: ns},
			Spec: apiv1.PerconaServerMySQLSpec{
				CRVersion: crVersion,
				MySQL: apiv1.MySQLSpec{
					ClusterType: apiv1.ClusterTypeGR,
					PodSpec:     apiv1.PodSpec{Size: podCount},
				},
			},
			Status: apiv1.PerconaServerMySQLStatus{State: state},
		}
	}

	newSTS := func(cr *apiv1.PerconaServerMySQL, lastApplied string) *appsv1.StatefulSet {
		annotations := make(map[string]string)
		if lastApplied != "" {
			annotations[naming.AnnotationLastAppliedConfig.String()] = lastApplied
		}

		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        mysql.Name(cr),
				Namespace:   cr.Namespace,
				Annotations: annotations,
			},
			Spec: appsv1.StatefulSetSpec{
				Selector: &metav1.LabelSelector{MatchLabels: mysql.MatchLabels(cr)},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: mysql.MatchLabels(cr),
						// a running STS always carries a hash already, so a restart
						// shows up as a changed value rather than an added key
						Annotations: map[string]string{
							naming.AnnotationConfigHash.String(): staleHash,
						},
					},
				},
			},
		}
	}

	newConfigMap := func(cr *apiv1.PerconaServerMySQL, data string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: mysql.ConfigMapName(cr), Namespace: cr.Namespace},
			Data:       map[string]string{mysql.CustomConfigKey: data},
		}
	}

	newAutoConfigMap := func(cr *apiv1.PerconaServerMySQL, data string) *corev1.ConfigMap {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: mysql.AutoConfigMapName(cr), Namespace: cr.Namespace},
			Data:       map[string]string{mysql.CustomConfigKey: data},
		}
	}

	newSecret := func(cr *apiv1.PerconaServerMySQL) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: cr.InternalSecretName(), Namespace: cr.Namespace},
			Data:       map[string][]byte{string(apiv1.UserConfigurator): []byte(configuratorPass)},
		}
	}

	podName := func(idx int) string {
		return fmt.Sprintf("%s-mysql-%d", crName, idx)
	}

	// newPods returns count ready mysql pods. The operator waits for every pod
	// it expects to be ready before it talks to mysql, so a case that wants the
	// change deferred asks for fewer than podCount.
	newPods := func(count int) []client.Object {
		cr := newCR("", "")

		objs := make([]client.Object, 0, count)
		for i := range count {
			objs = append(objs, &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      podName(i),
					Namespace: cr.Namespace,
					Labels:    mysql.MatchLabels(cr),
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					Conditions: []corev1.PodCondition{
						{Type: corev1.ContainersReady, Status: corev1.ConditionTrue},
					},
				},
			})
		}
		return objs
	}

	// world is what the API holds besides the CR, the ConfigMap and the
	// statefulset: the internal secret plus the given number of ready pods.
	world := func(running int) []client.Object {
		return append([]client.Object{newSecret(newCR("", ""))}, newPods(running)...)
	}

	// mysqlCmd is the command the operator must run inside the mysql container:
	// the configurator user, its password from the internal secret and the FQDN
	// of the pod being configured.
	mysqlCmd := func(cr *apiv1.PerconaServerMySQL, pod, stmt string) []string {
		return []string{
			"mysql",
			"--database", "performance_schema",
			"-p" + configuratorPass,
			"-u", string(apiv1.UserConfigurator),
			"-h", pod + "." + mysql.ServiceName(cr) + "." + cr.Namespace,
			"-e", stmt,
		}
	}

	configHash := func(confJSON string) string {
		return fmt.Sprintf("%x", md5.Sum([]byte(confJSON)))
	}

	tests := []struct {
		desc              string
		crVersion         string                 // defaults to 1.3.0
		state             apiv1.StatefulAppState // status.state
		mysqlState        apiv1.StatefulAppState // status.mysql.state; defaults to ready
		currentConfig     string
		autoConfig        string            // my.cnf in the ConfigMap; empty means no ConfigMap at all
		lastAppliedConfig string            // JSON string on the statefulset annotation; empty means absent
		stashedConfig     string            // JSON string the cr carries while the statefulset is recreated
		object            []client.Object   // what the API holds besides the CR, the ConfigMap and the statefulset; nil means a healthy cluster
		stmtErrs          map[string]string // stderr mysql answers a given statement with; drives the case on its own

		expectedStmts  []string // List of SET GLOBAL statements expected on all pods
		expectRestart  bool
		expectedError  error
		expectedConfig string // last applied config annotation expected on the statefulset afterwards
	}{
		{
			// SET GLOBAL support shipped after 1.2.0, so older clusters must be
			// left alone entirely - including the annotation.
			desc:              "version gate skips 1.2.0",
			crVersion:         "1.2.0",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			desc:              "version gate skips 1.1.0",
			crVersion:         "1.1.0",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			// A new cluster reads my.cnf on startup, so the config is only
			// recorded - no pod is worth talking to yet.
			desc:           "new cluster records config without touching mysql",
			state:          apiv1.StateNew,
			currentConfig:  "[mysqld]\nmax_connections=200\n",
			expectedConfig: `{"max_connections":"200"}`,
		},
		{
			desc:           "new cluster without config map records empty config",
			state:          apiv1.StateNew,
			expectedConfig: `{}`,
		},
		{
			// Cluster state does not decide whether mysql can be reconfigured:
			// a cluster sits in initializing while an unrelated component is
			// unhealthy, and its mysql pods are ready and reachable throughout.
			desc:              "initializing cluster is reconfigured",
			state:             apiv1.StateInitializing,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			expectedStmts:     []string{"SET GLOBAL max_connections=200"},
			expectedConfig:    `{"max_connections":"200"}`,
		},
		{
			// Same for error, which is what a failure anywhere else in the
			// reconcile leaves behind.
			desc:              "error state is reconfigured",
			state:             apiv1.StateError,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			expectedStmts:     []string{"SET GLOBAL max_connections=200"},
			expectedConfig:    `{"max_connections":"200"}`,
		},
		{
			// What does decide it is whether mysql itself is there to talk to. A
			// paused cluster has no pod at all, and the annotation must stay
			// untouched too: writing it would make the change look applied and it
			// would never be retried once the pods come back.
			desc:              "paused cluster is deferred",
			state:             apiv1.StatePaused,
			mysqlState:        apiv1.StatePaused,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			object:            world(0),
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			// A partly ready cluster is deferred for the same reason: applying
			// to some pods and recording it as done would leave the rest behind.
			desc:              "config is deferred until every pod is ready",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			object:            world(podCount - 1),
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			// Every pod can be ready before mysql is: the component reports ready
			// only once the operator is done with it, so until then a reconfigure
			// would race whatever else is still in flight.
			desc:              "config is deferred until mysql reports ready",
			state:             apiv1.StateInitializing,
			mysqlState:        apiv1.StateInitializing,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"100"}`,
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			desc:              "corrupt last applied annotation is returned",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: "not-json",
			expectedError:     errors.New("get last applied MySQL config"),
			expectedConfig:    "not-json",
		},
		{
			// The common case by far: nothing changed. Every reconcile must stay
			// silent instead of re-running SET GLOBAL on every pod.
			desc:              "unchanged config runs no statements",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\n",
			lastAppliedConfig: `{"max_connections":"200"}`,
			expectedConfig:    `{"max_connections":"200"}`,
		},
		{
			desc:              "added key is applied to every pod",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=200\nsql_mode=STRICT_TRANS_TABLES\n",
			lastAppliedConfig: `{"max_connections":"200"}`,
			expectedStmts:     []string{"SET GLOBAL sql_mode='STRICT_TRANS_TABLES'"},
			expectedConfig:    `{"max_connections":"200","sql_mode":"STRICT_TRANS_TABLES"}`,
		},
		{
			// Only the key whose value moved may be applied - not re-applying
			// untouched keys is the whole point of the last-applied annotation.
			desc:              "only changed keys are applied",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=300\nbinlog_expire_logs_seconds=604800\n",
			lastAppliedConfig: `{"binlog_expire_logs_seconds":"604800","max_connections":"200"}`,
			expectedStmts:     []string{"SET GLOBAL max_connections=300"},
			expectedConfig:    `{"binlog_expire_logs_seconds":"604800","max_connections":"300"}`,
		},
		{
			// Cluster upgraded from an operator that never wrote the annotation:
			// everything in my.cnf counts as new.
			desc:          "missing last applied annotation applies every key",
			state:         apiv1.StateReady,
			currentConfig: "[mysqld]\nmax_connections=200\nsql_mode=STRICT_TRANS_TABLES\n",
			expectedStmts: []string{
				"SET GLOBAL max_connections=200",
				"SET GLOBAL sql_mode='STRICT_TRANS_TABLES'",
			},
			expectedConfig: `{"max_connections":"200","sql_mode":"STRICT_TRANS_TABLES"}`,
		},
		{
			// Values reach mysql as SQL: numbers stay bare, byte suffixes are
			// expanded, booleans normalize and everything else is quoted with
			// embedded quotes doubled.
			desc:  "values are formatted for sql",
			state: apiv1.StateReady,
			currentConfig: "[mysqld]\n" +
				"max_connections=300\n" +
				"innodb_buffer_pool_size=2G\n" +
				"read_only=on\n" +
				"super_read_only=false\n" +
				"sql_mode=STRICT_TRANS_TABLES\n" +
				"init_connect=SET @x=it's\n",
			expectedStmts: []string{
				"SET GLOBAL max_connections=300",
				"SET GLOBAL innodb_buffer_pool_size=2147483648",
				"SET GLOBAL read_only=ON",
				"SET GLOBAL super_read_only=OFF",
				"SET GLOBAL sql_mode='STRICT_TRANS_TABLES'",
				`SET GLOBAL init_connect='SET @x=it''s'`,
			},
			expectedConfig: `{"init_connect":"SET @x=it's","innodb_buffer_pool_size":"2G",` +
				`"max_connections":"300","read_only":"on","sql_mode":"STRICT_TRANS_TABLES",` +
				`"super_read_only":"false"}`,
		},
		{
			desc:  "loose prefix is stripped from the statement",
			state: apiv1.StateReady,
			currentConfig: "[mysqld]\n" +
				"loose_group_replication_start_on_boot=off\n" +
				"loose-group_replication_consistency=BEFORE_ON_PRIMARY_FAILOVER\n" +
				"max_connections=300\n",
			lastAppliedConfig: `{"max_connections":"300"}`,
			expectedStmts: []string{
				"SET GLOBAL group_replication_start_on_boot=OFF",
				"SET GLOBAL group_replication_consistency='BEFORE_ON_PRIMARY_FAILOVER'",
			},
			expectedConfig: `{"loose-group_replication_consistency":"BEFORE_ON_PRIMARY_FAILOVER",` +
				`"loose_group_replication_start_on_boot":"off","max_connections":"300"}`,
		},
		{
			// A removed key cannot be un-set at runtime, so the only correct
			// answer is a restart - and no SQL at all, not even for the keys
			// that changed in the same edit.
			desc:              "removed key restarts instead of running statements",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=300\n",
			lastAppliedConfig: `{"max_connections":"200","sql_mode":"STRICT_TRANS_TABLES"}`,
			expectRestart:     true,
			expectedConfig:    `{"max_connections":"300"}`,
		},
		{
			desc:              "deleted config map restarts with an empty config",
			state:             apiv1.StateReady,
			lastAppliedConfig: `{"max_connections":"200"}`,
			expectRestart:     true,
			expectedConfig:    `{}`,
		},
		{
			// A removal is decided before pod state is consulted, so the restart is
			// requested with no pod running at all. Nothing is lost either way: a
			// pod that starts from here reads my.cnf.
			desc:              "removed key restarts with no pod running",
			state:             apiv1.StateInitializing,
			currentConfig:     "[mysqld]\nmax_connections=300\n",
			lastAppliedConfig: `{"max_connections":"200","sql_mode":"STRICT_TRANS_TABLES"}`,
			object:            world(0),
			expectRestart:     true,
			expectedConfig:    `{"max_connections":"300"}`,
		},
		{
			// mysql refuses a variable that cannot be changed at runtime, which
			// falls back to a restart - and the batch must still be tried on
			// every pod.
			desc:              "read only variable restarts and does not abort the batch",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=300\ninnodb_buffer_pool_size=2G\n",
			lastAppliedConfig: `{"max_connections":"200"}`,
			expectedStmts: []string{
				"SET GLOBAL max_connections=300",
				"SET GLOBAL innodb_buffer_pool_size=2147483648",
			},
			expectRestart:  true,
			expectedConfig: `{"innodb_buffer_pool_size":"2G","max_connections":"300"}`,
		},
		{
			// An SQL error that is not a refusal must leave the annotation
			// alone, otherwise the next reconcile treats the failed change as
			// applied and never retries it.
			desc:              "sql error is returned and config is not recorded",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=300\n",
			lastAppliedConfig: `{"max_connections":"200"}`,
			expectedStmts:     []string{"SET GLOBAL max_connections=300"},
			expectedError:     errors.New("set global variables on pod " + podName(0)),
			expectedConfig:    `{"max_connections":"200"}`,
		},
		{
			desc:              "auto config keys are applied to every pod",
			state:             apiv1.StateReady,
			autoConfig:        "\nmax_connections=442\nthread_cache_size=440",
			lastAppliedConfig: `{}`,
			expectedStmts: []string{
				"SET GLOBAL max_connections=442",
				"SET GLOBAL thread_cache_size=440",
			},
			expectedConfig: `{"max_connections":"442","thread_cache_size":"440"}`,
		},
		{
			desc:              "user configuration overrides the auto config",
			state:             apiv1.StateReady,
			autoConfig:        "\nmax_connections=442",
			currentConfig:     "[mysqld]\nmax_connections=100\n",
			lastAppliedConfig: `{}`,
			expectedStmts:     []string{"SET GLOBAL max_connections=100"},
			expectedConfig:    `{"max_connections":"100"}`,
		},
		{
			desc:              "auto config restarts when mysql refuses the value",
			state:             apiv1.StateReady,
			autoConfig:        "\ninnodb_buffer_pool_size=3512016613",
			lastAppliedConfig: `{"innodb_buffer_pool_size":"2147483648"}`,
			expectedStmts:     []string{"SET GLOBAL innodb_buffer_pool_size=3512016613"},
			expectRestart:     true,
			expectedConfig:    `{"innodb_buffer_pool_size":"3512016613"}`,
		},
		{
			desc:              "unknown loose variable is skipped",
			state:             apiv1.StateReady,
			autoConfig:        "\nloose_binlog_transaction_dependency_tracking=WRITESET\nmax_connections=442",
			lastAppliedConfig: `{}`,
			stmtErrs: map[string]string{
				"SET GLOBAL binlog_transaction_dependency_tracking='WRITESET'": stderrUnknown,
			},
			expectedStmts: []string{
				"SET GLOBAL binlog_transaction_dependency_tracking='WRITESET'",
				"SET GLOBAL max_connections=442",
			},
			expectedConfig: `{"loose_binlog_transaction_dependency_tracking":"WRITESET","max_connections":"442"}`,
		},
		{
			desc:              "unknown variable without the loose prefix fails",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connectons=100\n",
			lastAppliedConfig: `{}`,
			stmtErrs: map[string]string{
				"SET GLOBAL max_connectons=100": stderrUnknown,
			},
			expectedStmts:  []string{"SET GLOBAL max_connectons=100"},
			expectedError:  errors.New("unknown configuration variables: [max_connectons]"),
			expectedConfig: `{}`,
		},
		{
			desc:              "missing configurator password is returned",
			state:             apiv1.StateReady,
			currentConfig:     "[mysqld]\nmax_connections=300\n",
			lastAppliedConfig: `{"max_connections":"200"}`,
			object:            newPods(podCount), // everything but the internal secret
			expectedError:     errors.New("get operator password"),
			expectedConfig:    `{"max_connections":"200"}`,
		},
		{
			// A statefulset recreated to resize its volume claim template comes
			// back without the record, and re-applying a configuration mysqld
			// already has restarts the cluster over the variables that cannot be
			// set at runtime. The copy the cr carries stands in for it, so this
			// case must reach mysql with nothing to say - the mock fails the test
			// on any statement.
			desc:           "recreated statefulset takes the applied config from the cr",
			state:          apiv1.StateReady,
			currentConfig:  "[mysqld]\nmax_connections=200\n",
			stashedConfig:  `{"max_connections":"200"}`,
			expectedConfig: `{"max_connections":"200"}`,
		},
		{
			// The copy is a record of what was applied, not a licence to skip
			// the diff: a change made while the set was being recreated still
			// reaches mysql.
			desc:           "config changed during the recreate is still applied",
			state:          apiv1.StateReady,
			currentConfig:  "[mysqld]\nmax_connections=200\n",
			stashedConfig:  `{"max_connections":"100"}`,
			expectedStmts:  []string{"SET GLOBAL max_connections=200"},
			expectedConfig: `{"max_connections":"200"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			ctx := context.Background()
			scheme := newScheme(t)

			crVersion := tt.crVersion
			if crVersion == "" {
				crVersion = "1.3.0"
			}
			mysqlState := tt.mysqlState
			if mysqlState == "" {
				mysqlState = apiv1.StateReady
			}
			cr := newCR(crVersion, tt.state)
			cr.Status.MySQL.State = mysqlState
			if tt.stashedConfig != "" {
				cr.Annotations = map[string]string{
					naming.AnnotationLastAppliedConfig.String(): tt.stashedConfig,
				}
			}
			sts := newSTS(cr, tt.lastAppliedConfig)

			objs := []client.Object{cr, sts.DeepCopy()}
			if tt.currentConfig != "" {
				objs = append(objs, newConfigMap(cr, tt.currentConfig))
			}
			if tt.autoConfig != "" {
				objs = append(objs, newAutoConfigMap(cr, tt.autoConfig))
			}
			if tt.object != nil {
				objs = append(objs, tt.object...)
			} else {
				objs = append(objs, world(podCount)...)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).WithStatusSubresource(cr).Build()

			// What mysql answers every statement: a case expecting a restart
			// gets the refusal that leaves the operator no other way to apply
			// the value, a case expecting an error gets one that must abort.
			mysqlErr := ""
			switch {
			case len(tt.stmtErrs) > 0:
				// the case answers each statement itself
			case tt.expectRestart:
				mysqlErr = stderrReadOnly
			case tt.expectedError != nil:
				mysqlErr = stderrDenied
			}

			// Only a refusal is survivable, so anything else stops the run on
			// the first pod - pods are visited in name order.
			pods := podCount
			if mysqlErr == stderrDenied {
				pods = 1
			}

			// NewClient asserts on cleanup that every expectation below was
			// met, and the mock fails the test on any call not expected here.
			cliCmd := clientcmdmock.NewClient(t)
			for i := range pods {
				pod := podName(i)
				for _, stmt := range tt.expectedStmts {
					call := cliCmd.On("Exec",
						mock.Anything,
						mock.MatchedBy(func(p *corev1.Pod) bool { return p.Name == pod }),
						"mysql",
						mysqlCmd(cr, pod, stmt),
						mock.Anything, // stdin
						mock.Anything, // stdout
						mock.Anything, // stderr
						false,         // tty
					).Return(nil).Once()

					stderr := mysqlErr
					if e, ok := tt.stmtErrs[stmt]; ok {
						stderr = e
					}
					if stderr != "" {
						call.Run(func(args mock.Arguments) {
							_, _ = args.Get(6).(io.Writer).Write([]byte(stderr))
						})
					}
				}
			}

			r := &PerconaServerMySQLReconciler{Client: cl, Scheme: scheme, ClientCmd: cliCmd}

			err := r.reconcileMySQLConfig(ctx, cr, sts)
			if tt.expectedError != nil {
				require.ErrorContains(t, err, tt.expectedError.Error())
			} else {
				require.NoError(t, err)
			}

			updated := new(appsv1.StatefulSet)
			require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: mysql.Name(cr), Namespace: cr.Namespace}, updated))

			lastApplied, ok := updated.Annotations[naming.AnnotationLastAppliedConfig.String()]
			if tt.expectedConfig == "" {
				assert.False(t, ok, "last applied config annotation must be absent")
			} else {
				assert.Equal(t, tt.expectedConfig, lastApplied, "last applied config annotation")
			}

			// A restart shows up as the hash of the config just recorded, and
			// its absence as the hash the statefulset came in with.
			wantHash := staleHash
			if tt.expectRestart {
				wantHash = configHash(tt.expectedConfig)
			}
			assert.Equal(t, wantHash, updated.Spec.Template.Annotations[naming.AnnotationConfigHash.String()], "pod template config hash")

			// Whether the config is in sync is reported on the CR, but only once
			// the reconcile got far enough to know: a run that never reached the
			// pods leaves the condition alone.
			updatedCR := new(apiv1.PerconaServerMySQL)
			require.NoError(t, cl.Get(ctx, types.NamespacedName{Name: cr.Name, Namespace: cr.Namespace}, updatedCR))

			if tt.stashedConfig != "" {
				_, kept := updatedCR.GetAnnotations()[naming.AnnotationLastAppliedConfig.String()]
				assert.False(t, kept, "the copy on the cr is dropped once it is back on the statefulset")
			}
		})
	}
}
