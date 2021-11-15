package orchestrator

import (
	v2 "github.com/percona/percona-server-mysql-operator/api/v2"
	"github.com/percona/percona-server-mysql-operator/pkg/util"
)

func podSpec(cr *v2.PerconaServerForMySQL) *v2.PodSpec {
	return &cr.Spec.Orchestrator
}

// Labels returns labels of orchestrator
func Labels(cr *v2.PerconaServerForMySQL) map[string]string {
	return podSpec(cr).Labels
}

func matchLabels(cr *v2.PerconaServerForMySQL) map[string]string {
	return util.MergeSSMap(Labels(cr),
		map[string]string{v2.ComponentLabel: componentName},
		cr.Labels())
}
