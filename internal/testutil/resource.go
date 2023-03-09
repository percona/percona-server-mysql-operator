package testutil

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/yaml"

	apiv1alpha1 "github.com/percona/percona-server-mysql-operator/api/v1alpha1"
)

var (
	_, b, _, _ = runtime.Caller(0)
	deployPath = filepath.Join(filepath.Dir(b), "../../deploy")
)

type Resource interface {
	apiv1alpha1.PerconaServerMySQL | apiv1alpha1.PerconaServerMySQLBackup | apiv1alpha1.PerconaServerMySQLRestore
}

// DefaultResource returns a resource filled with values from `deploy` folder for the given type.
func DefaultResource[T Resource](cr *T) *T {
	filename := ""
	switch any(cr).(type) {
	case *apiv1alpha1.PerconaServerMySQL:
		filename = "cr.yaml"
	case *apiv1alpha1.PerconaServerMySQLBackup:
		filename = "backup.yaml"
	case *apiv1alpha1.PerconaServerMySQLRestore:
		filename = "restore.yaml"
	default:
		panic(errors.Errorf("unknown type %T", cr))
	}

	if err := unmarshalFileYAML(filepath.Join(deployPath, filename), cr); err != nil {
		panic(err)
	}
	return cr
}

func unmarshalFileYAML(filepath string, resource any) error {
	data, err := os.ReadFile(filepath)
	if err != nil {
		return errors.Wrapf(err, "failed to read %s", filepath)
	}

	if err := yaml.Unmarshal(data, resource); err != nil {
		return errors.Wrapf(err, "failed to unmarshal %s", filepath)
	}

	return nil
}

// UpdateResource updates the given resource with the given functions.
func UpdateResource[T Resource](cr *T, updateFuncs ...func(cr *T)) *T {
	for _, f := range updateFuncs {
		f(cr)
	}
	return cr
}
