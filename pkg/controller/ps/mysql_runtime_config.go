package ps

import (
	"context"
	"strings"

	"github.com/go-ini/ini"
	"github.com/pkg/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/clientcmd"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func applyMySQLRuntimeConfig(
	ctx context.Context,
	cl client.Client,
	clCmd clientcmd.Client,
	cr *apiv1.PerconaServerMySQL,
	conf ini.Section,
) (bool, error) {
	pods, err := k8s.PodsByLabels(ctx, cl, mysql.MatchLabels(cr), cr.GetNamespace())
	if err != nil {
		return false, errors.Wrap(err, "get pods by labels")
	}

	operatorPass, err := k8s.UserPassword(ctx, cl, cr, apiv1.UserOperator)
	if err != nil {
		return false, errors.Wrap(err, "get operator password")
	}

	for _, pod := range pods {
		for _, k := range conf.Keys() {
			key := k.Name()
			value := k.Value()
			manager := db.NewAdminManager(&pod, clCmd, apiv1.UserOperator, operatorPass, mysql.PodFQDN(cr, &pod))
			err := manager.SetGlobal(ctx, key, value)
			if err != nil {
				if strings.Contains(err.Error(), "ERROR 1238 (HY000)") {
					return true, nil
				}
				return false, errors.Wrapf(err, "set global variable %s=%s", key, value)
			}
		}
	}

	return false, nil
}
