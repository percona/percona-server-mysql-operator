package ps

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/config"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

func (r *PerconaServerMySQLReconciler) reconcileMySQLConfig(
	ctx context.Context,
	cr *apiv1.PerconaServerMySQL,
	sts *appsv1.StatefulSet,
) error {
	if cr.CompareVersion("1.2.0") <= 0 {
		return nil
	}

	log := logf.FromContext(ctx)
	conf, err := mysql.GetConfig(ctx, r.Client, cr)
	if err != nil {
		return errors.Wrap(err, "get MySQL config")
	}

	confJson, err := conf.IntoJSON()
	if err != nil {
		return errors.Wrap(err, "parse section into JSON")
	}

	writeAnnotation := func() error {
		if err := k8s.AnnotateObject(ctx, r.Client, sts, map[naming.AnnotationKey]string{
			naming.AnnotationLastAppliedConfig: string(confJson),
		}); err != nil {
			return errors.Wrap(err, "annotate object")
		}
		return nil
	}

	confHash := fmt.Sprintf("%x", md5.Sum(confJson))
	restartMySQL := func() error {
		return k8s.RolloutRestart(ctx, r.Client, sts, naming.AnnotationConfigHash, confHash)
	}

	// New cluster, pods will read the config from the ConfigMap on startup.
	if cr.Status.State == apiv1.StateNew {
		return writeAnnotation()
	}

	lastAppliedConf, err := mysql.GetLastAppliedConfig(sts)
	if err != nil {
		return errors.Wrap(err, "get last applied MySQL config")
	}

	// If any keys are removed, trigger restart and return early.
	toRemove := lastAppliedConf.Subtract(conf)
	if len(toRemove) > 0 {
		log.Info("Variables have been removed, restart needed", "variables", toRemove)
		if err := restartMySQL(); err != nil {
			return errors.Wrap(err, "restart MySQL")
		}
		if err := writeAnnotation(); err != nil {
			return errors.Wrap(err, "write last applied config annotation")
		}
		return nil
	}

	toApply := conf.Subtract(lastAppliedConf)
	toApply = append(toApply, conf.Changed(lastAppliedConf)...)

	if len(toApply) == 0 {
		return nil
	}

	pods, err := k8s.RunningPods(ctx, r.Client, mysql.MatchLabels(cr), cr.GetNamespace())
	if err != nil {
		return errors.Wrap(err, "get running pods")
	}

	// We want all pods running before we exec and apply the config.
	if cr.Spec.Pause || len(pods) < int(cr.Spec.MySQL.Size) {
		log.Info("Not all pods are running, defer applying configuration", "running", len(pods), "desired", cr.Spec.MySQL.Size)
		return nil
	}

	log.Info("Setting MySQL configuration", "variables", toApply)

	if restartNeeded, err := setGlobalVariables(ctx, r.Client, r.ClientCmd, cr, &conf, toApply, pods); err != nil {
		return errors.Wrap(err, "set global variables")
	} else if restartNeeded {
		log.Info("One or more variables require MySQL restart to take effect", "variables", toApply)
		if err := restartMySQL(); err != nil {
			return errors.Wrap(err, "restart MySQL")
		}
	}

	if err := writeAnnotation(); err != nil {
		return errors.Wrap(err, "write last applied config annotation")
	}
	return nil
}

const (
	readOnlyErrorString                = "ERROR 1238"
	unknownVariableErrorString         = "ERROR 1193"
	groupReplicationRunningErrorString = "ERROR 3093"
)

func isReadOnlyVariableError(err error) bool {
	return strings.Contains(err.Error(), readOnlyErrorString)
}

func isUnknownVariableError(err error) bool {
	return strings.Contains(err.Error(), unknownVariableErrorString)
}

func isGRRunningVariableError(err error) bool {
	return strings.Contains(err.Error(), groupReplicationRunningErrorString)
}

func setGlobalVariables(
	ctx context.Context,
	cl client.Client,
	clCmd clientcmd.Client,
	cr *apiv1.PerconaServerMySQL,
	conf *config.Section,
	keys []string,
	pods []corev1.Pod,
) (bool, error) {
	pass, err := k8s.UserPassword(ctx, cl, cr, apiv1.UserConfigurator)
	if err != nil {
		return false, errors.Wrap(err, "get operator password")
	}

	kv := make(map[string]string)
	for _, k := range keys {
		key, err := conf.GetKey(k)
		if err != nil {
			return false, errors.Wrapf(err, "get key %s", k)
		}
		kv[k] = mysql.FormatConfigValue(key.Value())
	}

	unknownVariables := map[string]struct{}{}
	restartNeeded := false
	for _, pod := range pods {
		mgr := db.NewAdminManager(&pod, clCmd, apiv1.UserConfigurator, pass, mysql.PodFQDN(cr, &pod))
		for k, v := range kv {
			err := mgr.SetGlobalVariable(ctx, k, v)
			if err != nil {
				if isReadOnlyVariableError(err) || isGRRunningVariableError(err) {
					restartNeeded = true
					continue
				}
				if isUnknownVariableError(err) {
					unknownVariables[k] = struct{}{}
					continue
				}
				return false, errors.Wrapf(err, "set global variables on pod %s", pod.Name)
			}
		}
	}

	printUnknownVariables := func() string {
		keys := make([]string, 0, len(unknownVariables))
		for k := range unknownVariables {
			keys = append(keys, k)
		}
		return strings.Join(keys, ", ")
	}

	log := logf.FromContext(ctx)
	if len(unknownVariables) > 0 {
		err := fmt.Errorf("unknown configuration variables: [%s]", printUnknownVariables())
		log.Error(err, "setGlobalVariables failed", "unknownVariables", printUnknownVariables())
		return false, err
	}

	return restartNeeded, nil
}
