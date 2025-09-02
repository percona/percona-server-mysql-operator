package ps

import (
	"bytes"
	"context"
	"slices"
	"strings"

	"github.com/go-ini/ini"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
)

func (r *PerconaServerMySQLReconciler) reconcileDataSource(ctx context.Context, cr *apiv1alpha1.PerconaServerMySQL) error {
	log := logf.FromContext(ctx).WithName("reconcileDataSource")

	pvcSpec := cr.Spec.MySQL.VolumeSpec.PersistentVolumeClaim
	if pvcSpec.DataSource == nil && pvcSpec.DataSourceRef == nil {
		return nil
	}

	if cr.Spec.MySQL.ClusterType != apiv1alpha1.ClusterTypeAsync || cr.Status.State != apiv1alpha1.StateInitializing {
		return nil
	}

	pods, err := k8s.PodsByLabels(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get pods")
	}

	slices.SortFunc(pods, func(a, b corev1.Pod) int {
		return strings.Compare(a.Name, b.Name)
	})

	uuidMap := make(map[string]struct{})

	for _, pod := range pods {
		if !k8s.IsPodReady(pod) {
			continue
		}

		var outb, errb bytes.Buffer
		cmd := []string{"cat", "/var/lib/mysql/auto.cnf"}
		if err := r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false); err != nil {
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}

		f, err := ini.Load(outb.Bytes())
		if err != nil {
			return errors.Wrap(err, "ini load")
		}
		section := f.Section("auto")
		if section == nil || section.Key("server-uuid") == nil {
			continue
		}

		uuid := section.Key("server-uuid").String()
		if _, ok := uuidMap[uuid]; !ok {
			uuidMap[uuid] = struct{}{}
			continue
		}

		outb.Reset()
		errb.Reset()
		log.Info("Pod has duplicate server-uuid. Removing /var/lib/mysql/auto.cnf and deleting the pod", "pod", pod.Name, "uuid", uuid)
		cmd = []string{"rm", "/var/lib/mysql/auto.cnf"}
		if err := r.ClientCmd.Exec(ctx, &pod, "mysql", cmd, nil, &outb, &errb, false); err != nil {
			return errors.Wrapf(err, "run %s, stdout: %s, stderr: %s", cmd, outb.String(), errb.String())
		}
		if err := r.Delete(ctx, &pod); err != nil {
			return errors.Wrap(err, "delete pod")
		}
	}

	return nil
}
