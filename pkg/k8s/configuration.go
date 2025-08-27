package k8s

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

type Configurable interface {
	GetConfigMapName() string
	GetConfigMapKey() string
	GetConfiguration() string
	GetResources() corev1.ResourceRequirements
	ExecuteConfigurationTemplate(configuration string, memory *resource.Quantity) (string, error)
}

func CustomConfigHash(ctx context.Context, cl client.Client, cr *apiv1alpha1.PerconaServerMySQL, configurable Configurable) (string, error) {
	log := logf.FromContext(ctx).WithName("CustomConfigHash")

	cmName := configurable.GetConfigMapName()
	nn := types.NamespacedName{Name: cmName, Namespace: cr.Namespace}
	currCm := &corev1.ConfigMap{}
	if err := cl.Get(ctx, nn, currCm); err != nil && !k8serrors.IsNotFound(err) {
		return "", errors.Wrapf(err, "get ConfigMap/%s", cmName)
	}

	if configurable.GetConfiguration() == "" {
		exists, err := ObjectExists(ctx, cl, nn, currCm)
		if err != nil {
			return "", errors.Wrapf(err, "check if ConfigMap/%s exists", cmName)
		}

		if !exists {
			return "", nil
		}

		if exists && !metav1.IsControlledBy(currCm, cr) {
			// ConfigMap exists and is created by the user, not the operator

			d := struct{ Data map[string]string }{Data: currCm.Data}
			data, err := json.Marshal(d)

			if cmName == cr.Name+"-mysql" && currCm.Data["my.cnf"] == "" {
				return "", errors.New("Failed to update config map. Please use my.cnf as a config name. Only in this case config map will be applied to the cluster")
			}

			if err != nil {
				return "", errors.Wrap(err, "marshal configmap data to json")
			}

			return fmt.Sprintf("%x", md5.Sum(data)), nil
		}

		if err := cl.Delete(ctx, currCm); err != nil {
			return "", errors.Wrapf(err, "delete ConfigMaps/%s", cmName)
		}

		log.Info("ConfigMap deleted", "name", cmName)

		return "", nil
	}

	var memory *resource.Quantity
	if res := configurable.GetResources(); res.Size() > 0 {
		if _, ok := res.Requests[corev1.ResourceMemory]; ok {
			memory = res.Requests.Memory()
		}
		if _, ok := res.Limits[corev1.ResourceMemory]; ok {
			memory = res.Limits.Memory()
		}
	}

	configuration := configurable.GetConfiguration()
	if memory != nil {
		var err error
		configuration, err = configurable.ExecuteConfigurationTemplate(configurable.GetConfiguration(), memory)
		if err != nil {
			return "", errors.Wrap(err, "execute configuration template")
		}
	} else if strings.Contains(configuration, "{{") {
		return "", errors.New("resources.limits[memory] or resources.requests[memory] should be specified for template usage in configuration")
	}

	cm := ConfigMap(cr, cmName, configurable.GetConfigMapKey(), configuration)
	if !reflect.DeepEqual(currCm.Data, cm.Data) {
		if err := EnsureObject(ctx, cl, cr, cm, cl.Scheme()); err != nil {
			return "", errors.Wrapf(err, "ensure ConfigMap/%s", cmName)
		}

		log.Info("ConfigMap updated", "name", cmName, "data", cm.Data)
	}

	d := struct{ Data map[string]string }{Data: cm.Data}
	data, err := json.Marshal(d)
	if err != nil {
		return "", errors.Wrap(err, "marshal configmap data to json")
	}

	return fmt.Sprintf("%x", md5.Sum(data)), nil
}
