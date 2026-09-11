package ps

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/db"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/mysql"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// reconcileTLSReload picks up a rotated leaf certificate on the running MySQL pods.
func (r *PerconaServerMySQLReconciler) reconcileTLSReload(ctx context.Context, cr *apiv1.PerconaServerMySQL, sts *appsv1.StatefulSet) error {
	if cr.CompareVersion("1.3.0") < 0 {
		return nil
	}

	log := logf.FromContext(ctx).WithName("reconcileTLSReload")

	secret := new(corev1.Secret)
	if err := r.Get(ctx, types.NamespacedName{
		Name:      cr.Spec.SSLSecretName,
		Namespace: cr.Namespace,
	}, secret); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrap(err, "get TLS secret")
	}

	certHash := tlsCertHash(secret)
	if certHash == "" {
		return nil
	}

	writeAnnotation := func() error {
		if err := k8s.AnnotateObject(ctx, r.Client, sts, map[naming.AnnotationKey]string{
			naming.AnnotationLastReloadedTLS: certHash,
		}); err != nil {
			return errors.Wrap(err, "annotate object")
		}
		return nil
	}

	lastReloaded, ok := sts.Annotations[naming.AnnotationLastReloadedTLS.String()]

	// Pods read the certificates on startup, so a cluster with no hash recorded
	// yet only needs something to compare against on the next rotation.
	if !ok || cr.Status.State == apiv1.StateNew {
		return writeAnnotation()
	}

	if lastReloaded == certHash {
		return nil
	}

	pods, err := k8s.RunningPods(ctx, r.Client, mysql.MatchLabels(cr), cr.Namespace)
	if err != nil {
		return errors.Wrap(err, "get running pods")
	}

	if cr.Spec.Pause || len(pods) < int(cr.Spec.MySQL.Size) {
		log.Info("Not all pods are running, defer reloading TLS certificates", "running", len(pods), "desired", cr.Spec.MySQL.Size)
		return nil
	}

	// kubelet refreshes a mounted secret on its own schedule, so a pod can still
	// hold the previous certificate, and reloading it now would re-read the old file.
	for _, pod := range pods {
		onDisk, err := r.tlsCertHashOnPod(ctx, &pod)
		if err != nil {
			return errors.Wrapf(err, "get certificate from pod %s", pod.Name)
		}
		if onDisk != certHash {
			// Certificate is not propagated to the pod yet, defer reloading TLS certificates
			return nil
		}
	}

	operatorPass, err := k8s.UserPassword(ctx, r.Client, cr, apiv1.UserOperator)
	if err != nil {
		return errors.Wrap(err, "get operator password")
	}

	for _, pod := range pods {
		mgr := db.NewAdminManager(&pod, r.ClientCmd, apiv1.UserOperator, operatorPass, mysql.PodFQDN(cr, &pod))
		if err := mgr.ReloadTLS(ctx); err != nil {
			return errors.Wrapf(err, "reload TLS on pod %s", pod.Name)
		}
	}

	log.Info("Reloaded TLS certificates without restarting pods", "pods", len(pods))

	return writeAnnotation()
}

func tlsCertHash(secret *corev1.Secret) string {
	cert, key := secret.Data[naming.TLSCertKey], secret.Data[naming.TLSKeyKey]
	if len(cert) == 0 || len(key) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", md5.Sum(slices.Concat(cert, key)))
}

func (r *PerconaServerMySQLReconciler) tlsCertHashOnPod(ctx context.Context, pod *corev1.Pod) (string, error) {
	cmd := []string{
		"cat",
		filepath.Join(mysql.TLSMountPath, naming.TLSCertKey),
		filepath.Join(mysql.TLSMountPath, naming.TLSKeyKey),
	}

	var stdout, stderr bytes.Buffer
	if err := r.ClientCmd.Exec(ctx, pod, "mysql", cmd, nil, &stdout, &stderr, false); err != nil {
		return "", errors.Wrapf(err, "stderr: %s", stderr.String())
	}

	return fmt.Sprintf("%x", md5.Sum(stdout.Bytes())), nil
}
