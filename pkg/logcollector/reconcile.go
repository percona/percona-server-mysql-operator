package logcollector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
	"github.com/percona/percona-server-mysql-operator/pkg/k8s"
	"github.com/percona/percona-server-mysql-operator/pkg/logcollector/logrotate"
	"github.com/percona/percona-server-mysql-operator/pkg/naming"
)

// Reconcile resolves the log collector's default enabled state and reconciles
// the ConfigMaps backing its configuration. mysqlSTS identifies the cluster's
// MySQL StatefulSet, used to tell a new cluster from an existing one.
func Reconcile(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, mysqlSTS types.NamespacedName) error {
	if cr.CompareVersion("1.3.0") < 0 {
		return nil
	}

	if err := resolveDefaultEnabled(ctx, cl, cr, mysqlSTS); err != nil {
		return errors.Wrap(err, "resolve log collector default")
	}

	if err := reconcileFluentBitConfigMap(ctx, cl, cr); err != nil {
		return errors.Wrap(err, "fluent-bit config map")
	}

	if err := reconcileLogRotateConfigMap(ctx, cl, cr); err != nil {
		return errors.Wrap(err, "logrotate config map")
	}

	return nil
}

// resolveDefaultEnabled defaults an unset Enabled to on for new clusters and off
// for existing ones, so upgrading the operator does not roll running pods. The
// decision is recorded on the cluster because the signal it derives from -
// whether the MySQL StatefulSet exists - stops holding once this reconcile
// creates that StatefulSet.
func resolveDefaultEnabled(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, mysqlSTS types.NamespacedName) error {
	if cr.Spec.LogCollector == nil || cr.Spec.LogCollector.Enabled != nil {
		return nil
	}

	if decided, ok := cr.Annotations[string(naming.AnnotationLogCollectorDefaulted)]; ok {
		enabled := decided == "true"
		cr.Spec.LogCollector.Enabled = &enabled
		return nil
	}

	err := cl.Get(ctx, mysqlSTS, new(appsv1.StatefulSet))
	if err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "get StatefulSet/%s", mysqlSTS.Name)
	}

	isNewCluster := k8serrors.IsNotFound(err)

	orig := cr.DeepCopy()
	if cr.Annotations == nil {
		cr.Annotations = make(map[string]string)
	}
	cr.Annotations[string(naming.AnnotationLogCollectorDefaulted)] = strconv.FormatBool(isNewCluster)

	if err := cl.Patch(ctx, cr, client.MergeFrom(orig)); err != nil {
		return errors.Wrap(err, "record log collector default")
	}

	cr.Spec.LogCollector.Enabled = &isNewCluster

	return nil
}

// ConfigHash digests the log collector configuration, including the contents of
// the ConfigMaps it references.
func ConfigHash(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) (string, error) {
	if !cr.LogCollectorEnabled() {
		return "", nil
	}

	payload := struct {
		FluentBit       string                       `json:"fluentBit"`
		LogRotate       string                       `json:"logRotate"`
		Schedule        string                       `json:"schedule"`
		ExtraConfigData map[string]map[string]string `json:"extraConfigData"`
	}{
		FluentBit:       cr.Spec.LogCollector.Configuration,
		ExtraConfigData: make(map[string]map[string]string),
	}
	if lr := cr.Spec.LogCollector.LogRotate; lr != nil {
		payload.LogRotate = lr.Configuration
		payload.Schedule = lr.Schedule
	}

	for _, name := range cr.LogRotateExtraConfigMaps() {
		cm := new(corev1.ConfigMap)
		err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: cr.Namespace}, cm)
		if err != nil && !k8serrors.IsNotFound(err) {
			return "", errors.Wrapf(err, "get ConfigMap/%s", name)
		}
		payload.ExtraConfigData[name] = cm.Data
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "marshal log collector config")
	}

	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// StampConfigHash records the log collector configuration hash on the pod
// template, so that editing the configuration rolls the pods.
func StampConfigHash(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, tmpl *corev1.PodTemplateSpec) error {
	hash, err := ConfigHash(ctx, cl, cr)
	if err != nil {
		return errors.Wrap(err, "compute log collector config hash")
	}
	if hash == "" {
		return nil
	}

	if tmpl.Annotations == nil {
		tmpl.Annotations = make(map[string]string)
	}
	tmpl.Annotations[string(naming.AnnotationLogCollectorConfigHash)] = hash

	return nil
}

func reconcileFluentBitConfigMap(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) error {
	name := ConfigMapName(cr.Name)

	if !cr.LogCollectorEnabled() || cr.Spec.LogCollector.Configuration == "" {
		return deleteConfigMapIfExists(ctx, cl, cr, name)
	}

	cm := k8s.ConfigMap(cr, name, fluentBitCustomConfigurationFile, cr.Spec.LogCollector.Configuration, naming.ComponentDatabase)

	return ensureConfigMap(ctx, cl, cr, cm)
}

func reconcileLogRotateConfigMap(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) error {
	name := logrotate.ConfigMapName(cr.Name)

	if !cr.LogCollectorEnabled() ||
		cr.Spec.LogCollector.LogRotate == nil ||
		cr.Spec.LogCollector.LogRotate.Configuration == "" {
		return deleteConfigMapIfExists(ctx, cl, cr, name)
	}

	cm := k8s.ConfigMap(cr, name, logrotate.MySQLConfig, cr.Spec.LogCollector.LogRotate.Configuration, naming.ComponentDatabase)

	return ensureConfigMap(ctx, cl, cr, cm)
}

func ensureConfigMap(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, desired *corev1.ConfigMap) error {
	existing := new(corev1.ConfigMap)
	err := cl.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, existing)
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "get ConfigMap/%s", desired.Name)
	}

	if err == nil && k8s.EqualConfigMaps(existing, desired) {
		return nil
	}

	if err := k8s.EnsureObjectWithHash(ctx, cl, cr, desired, cl.Scheme()); err != nil {
		return errors.Wrapf(err, "ensure ConfigMap/%s", desired.Name)
	}

	return nil
}

// deleteConfigMapIfExists removes a ConfigMap the operator owns, leaving
// ConfigMaps owned by anyone else alone.
func deleteConfigMapIfExists(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL, name string) error {
	cm := new(corev1.ConfigMap)
	err := cl.Get(ctx, types.NamespacedName{Name: name, Namespace: cr.Namespace}, cm)
	if k8serrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "get ConfigMap/%s", name)
	}

	if !metav1.IsControlledBy(cm, cr) {
		return nil
	}

	if err := cl.Delete(ctx, cm); err != nil && !k8serrors.IsNotFound(err) {
		return errors.Wrapf(err, "delete ConfigMap/%s", name)
	}

	return nil
}
