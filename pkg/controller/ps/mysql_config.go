package ps

import (
	"context"
	"crypto/md5"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
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

	if cr.Status.State != apiv1.StateReady {
		return nil
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

	restartNeeded := false
	if len(toApply) > 0 {
		log.Info("Setting MySQL configuration", "variables", toApply)
		restartNeeded, err = setGlobalVariables(ctx, r.Client, r.ClientCmd, cr, &conf, toApply)
		if err != nil {
			return errors.Wrap(err, "set global variables")
		}
	}

	if restartNeeded {
		log.Info("Restart needed after setting MySQL configuration")
		if err := restartMySQL(); err != nil {
			return errors.Wrap(err, "restart MySQL")
		}
	}

	if err := writeAnnotation(); err != nil {
		return errors.Wrap(err, "write last applied config annotation")
	}

	return nil
}

func setGlobalVariables(
	ctx context.Context,
	cl client.Client,
	clCmd clientcmd.Client,
	cr *apiv1.PerconaServerMySQL,
	conf *config.Section,
	keys []string,
) (bool, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr), cr.GetNamespace())
	if err != nil {
		return false, errors.Wrap(err, "get pods by labels")
	}

	operatorPass, err := k8s.UserPassword(ctx, cl, cr, apiv1.UserOperator)
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

	restartNeeded := false
	for _, pod := range pods {
		mgr := db.NewAdminManager(&pod, clCmd, apiv1.UserOperator, operatorPass, mysql.PodFQDN(cr, &pod))
		for k, v := range kv {
			err := mgr.SetGlobalVariables(ctx, k, v)
			if err != nil {
				if strings.Contains(err.Error(), "ERROR 1238") {
					restartNeeded = true
					continue
				}
				return false, errors.Wrapf(err, "set global variables on pod %s", pod.Name)
			}
		}
	}

	return restartNeeded, nil
}
