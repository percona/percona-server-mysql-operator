package main

import (
	"strings"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	metricsServer "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	ctrlWebhook "sigs.k8s.io/controller-runtime/pkg/webhook"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

const defaultElectionID = "08db2feb.percona.com"

type operatorOptions struct {
	operatorNamespace string

	options ctrl.Options
	env     util.EnvConfig
}

func configureOptions(metricsAddr, probeAddr string, enableLeaderElection bool) (ctrl.Options, error) {
	opts := operatorOptions{
		options: ctrl.Options{
			Scheme: scheme,
			Metrics: metricsServer.Options{
				BindAddress: metricsAddr,
			},
			HealthProbeBindAddress: probeAddr,
			WebhookServer: ctrlWebhook.NewServer(ctrlWebhook.Options{
				Port: 9443,
			}),
		},
	}

	var err error
	opts.env, err = util.GetEnvConfig()
	if err != nil {
		return opts.options, errors.Wrap(err, "failed to parse env vars")
	}
	if err := opts.configureNamespaces(); err != nil {
		return opts.options, errors.Wrap(err, "failed to configure namespaces")
	}
	if err := opts.configureLeaderElection(enableLeaderElection); err != nil {
		return opts.options, errors.Wrap(err, "failed to configure leader election")
	}
	if err := opts.configureGroupKindConcurrency(); err != nil {
		return opts.options, errors.Wrap(err, "failed to configure group kind concurrency")
	}

	return opts.options, nil
}

func (o *operatorOptions) configureNamespaces() error {
	if o.env.OperatorNamespace != nil {
		o.operatorNamespace = *o.env.OperatorNamespace
	} else {
		var err error
		o.operatorNamespace, err = k8s.DefaultAPINamespace()
		if err != nil {
			return errors.Wrap(err, "failed to get operators' namespace")
		}
	}

	// Add support for MultiNamespace set in WATCH_NAMESPACE.
	if o.env.WatchNamespace != "" {
		namespaces := make(map[string]cache.Config)
		for _, ns := range append(strings.Split(o.env.WatchNamespace, ","), o.operatorNamespace) {
			namespaces[ns] = cache.Config{}
		}
		o.options.Cache.DefaultNamespaces = namespaces
	}
	return nil
}

func (o *operatorOptions) configureLeaderElection(enableLeaderElectionFlag bool) error {
	o.options.LeaderElection = enableLeaderElectionFlag
	if o.env.LeaderElection != nil {
		o.options.LeaderElection = *o.env.LeaderElection
	}

	o.options.LeaderElectionID = defaultElectionID
	if o.options.LeaderElection {
		o.options.LeaderElectionNamespace = o.operatorNamespace
		if name := o.env.LeaderElectionID; name != "" {
			if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
				return errors.Errorf("PSO_LEADER_ELECTION_LEASE_NAME must be a valid DNS subdomain: %s", strings.Join(errs, ", "))
			}
			o.options.LeaderElectionID = name
		}
	}

	o.options.LeaseDuration = &o.env.LeaseDuration
	o.options.RenewDeadline = &o.env.RenewDeadline
	o.options.RetryPeriod = &o.env.RetryPeriod
	return nil
}

func (o *operatorOptions) configureGroupKindConcurrency() error {
	groupKinds := []string{
		"PerconaServerMySQL." + apiv1.GroupVersion.Group,
		"PerconaServerMySQLBackup." + apiv1.GroupVersion.Group,
		"PerconaServerMySQLRestore." + apiv1.GroupVersion.Group,
		"PerconaServerMySQLClusterSet." + apiv1.GroupVersion.Group,
	}

	const defaultConcurrency = 1
	o.options.Controller.GroupKindConcurrency = make(map[string]int, len(groupKinds))
	for _, gk := range groupKinds {
		o.options.Controller.GroupKindConcurrency[gk] = defaultConcurrency
	}

	if o.env.Workers != nil {
		if *o.env.Workers <= 0 {
			return errors.Errorf("MAX_CONCURRENT_RECONCILES must be a positive number: %d", *o.env.Workers)
		}
		for _, gk := range groupKinds {
			o.options.Controller.GroupKindConcurrency[gk] = *o.env.Workers
		}
	}
	return nil
}
