/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"os"
	"strconv"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	cmscheme "github.com/cert-manager/cert-manager/pkg/client/clientset/versioned/scheme"
	"github.com/go-logr/logr"
	uzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlWebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/controller/ps"
	"github.com/percona/percona-server-mysql-operator/pkg/controller/psbackup"
	"github.com/percona/percona-server-mysql-operator/pkg/controller/psrestore"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/platform"
	"github.com/percona/percona-server-mysql-operator/pkg/xtrabackup/storage"
	//+kubebuilder:scaffold:imports
)

var (
	GitCommit string
	BuildTime string
	scheme    = runtime.NewScheme()
	setupLog  = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(apiv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")

	opts := zap.Options{
		Encoder: getLogEncoder(setupLog),
		Level:   getLogLevel(setupLog),
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	setupLog.Info("Build info", "GitCommit", GitCommit, "BuildTime", BuildTime)

	namespace, err := k8s.GetWatchNamespace()
	if err != nil {
		setupLog.Error(err, "unable to get watch namespace")
		os.Exit(1)
	}

	operatorNamespace, err := k8s.GetOperatorNamespace()
	if err != nil {
		setupLog.Error(err, "failed to get operators' namespace")
		os.Exit(1)
	}
	options := ctrl.Options{
		Scheme: scheme,
		Metrics: metricsServer.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "08db2feb.percona.com",

		WebhookServer: ctrlWebhook.NewServer(ctrlWebhook.Options{
			Port: 9443,
		}),
	}

	// Add support for MultiNamespace set in WATCH_NAMESPACE
	if len(namespace) > 0 {
		namespaces := make(map[string]cache.Config)
		for _, ns := range append(strings.Split(namespace, ","), operatorNamespace) {
			namespaces[ns] = cache.Config{}
		}
		options.Cache.DefaultNamespaces = namespaces
	}

	// Get a config to talk to the apiserver
	config, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "")
		os.Exit(1)
	}
	mgr, err := ctrl.NewManager(config, options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	nsClient := client.NewNamespacedClient(mgr.GetClient(), namespace)

	cliCmd, err := clientcmd.NewClient()
	if err != nil {
		setupLog.Error(err, "unable to create clientcmd")
		os.Exit(1)
	}

	serverVersion, err := platform.GetServerVersion(cliCmd)
	if err != nil {
		setupLog.Error(err, "unable to get server version")
		os.Exit(1)
	}
	// Setup Scheme for cert-manager resources
	if err := cmscheme.AddToScheme(mgr.GetScheme()); err != nil {
		setupLog.Error(err, "unable to add cert-manager scheme")
		os.Exit(1)
	}

	if err = (&ps.PerconaServerMySQLReconciler{
		Client:        nsClient,
		Scheme:        mgr.GetScheme(),
		ServerVersion: serverVersion,
		Recorder:      mgr.GetEventRecorderFor("ps-controller"),
		ClientCmd:     cliCmd,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "ps-controller")
		os.Exit(1)
	}
	if err = (&psbackup.PerconaServerMySQLBackupReconciler{
		Client:        nsClient,
		Scheme:        mgr.GetScheme(),
		ServerVersion: serverVersion,
		ClientCmd:     cliCmd,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PerconaServerMySQLBackup")
		os.Exit(1)
	}
	if err = (&psrestore.PerconaServerMySQLRestoreReconciler{
		Client:           nsClient,
		Scheme:           mgr.GetScheme(),
		ServerVersion:    serverVersion,
		NewStorageClient: storage.NewClient,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PerconaServerMySQLRestore")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info(
		"starting manager",
		"GitCommit", GitCommit,
		"BuildTime", BuildTime,
		"Platform", serverVersion.Platform,
		"Version", serverVersion.Info,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func getLogEncoder(log logr.Logger) zapcore.Encoder {
	consoleEnc := zapcore.NewConsoleEncoder(uzap.NewDevelopmentEncoderConfig())

	s, found := os.LookupEnv("LOG_STRUCTURED")
	if !found {
		return consoleEnc
	}

	useJson, err := strconv.ParseBool(s)
	if err != nil {
		log.Info("Can't parse LOG_STRUCTURED env var, using console logger", "envVar", s)
		return consoleEnc
	}
	if !useJson {
		return consoleEnc
	}

	return zapcore.NewJSONEncoder(uzap.NewProductionEncoderConfig())
}

func getLogLevel(log logr.Logger) zapcore.LevelEnabler {
	l, found := os.LookupEnv("LOG_LEVEL")
	if !found {
		return zapcore.InfoLevel
	}

	switch strings.ToUpper(l) {
	case "DEBUG":
		return zapcore.DebugLevel
	case "INFO":
		return zapcore.InfoLevel
	case "ERROR":
		return zapcore.ErrorLevel
	default:
		log.Info("Unsupported log level, using INFO", "level", l)
		return zapcore.InfoLevel
	}
}
