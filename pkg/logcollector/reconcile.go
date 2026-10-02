package logcollector

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/pkg/errors"
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

// Reconcile reconciles the ConfigMaps backing the log collector configuration.
func Reconcile(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) error {
	if cr.CompareVersion("1.3.0") < 0 {
		return nil
	}

	if err := reconcileFluentBitConfigMap(ctx, cl, cr); err != nil {
		return errors.Wrap(err, "fluent-bit config map")
	}

	if err := reconcileLogRotateConfigMap(ctx, cl, cr); err != nil {
		return errors.Wrap(err, "logrotate config map")
	}

	return nil
}

// ConfigHash digests the log collector configuration, including the contents of
// the ConfigMaps it references.
func ConfigHash(ctx context.Context, cl client.Client, cr *apiv1.PerconaServerMySQL) (string, error) {
	if !cr.LogCollectorEnabled() {
		return "", nil
	}

	type configMapData struct {
		Data       map[string]string `json:"data"`
		BinaryData map[string][]byte `json:"binaryData"`
	}

	payload := struct {
		FluentBit       string                   `json:"fluentBit"`
		LogRotate       string                   `json:"logRotate"`
		Schedule        string                   `json:"schedule"`
		ExtraConfigData map[string]configMapData `json:"extraConfigData"`
	}{
		FluentBit:       cr.Spec.LogCollector.Configuration,
		ExtraConfigData: make(map[string]configMapData),
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
		payload.ExtraConfigData[name] = configMapData{Data: cm.Data, BinaryData: cm.BinaryData}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return "", errors.Wrap(err, "marshal log collector config")
	}

	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
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

	if err == nil {
		if !metav1.IsControlledBy(existing, cr) {
			return errors.Errorf("ConfigMap/%s already exists and is not controlled by this cluster", desired.Name)
		}

		if k8s.EqualConfigMaps(existing, desired) {
			return nil
		}
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
