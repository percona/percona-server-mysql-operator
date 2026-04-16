package tls

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1 "github.com/percona/percona-server-mysql-operator/api/v1"
)

func TestDNSNames(t *testing.T) {
	tests := map[string]struct {
		cr       *apiv1.PerconaServerMySQL
		expected map[string]struct{}
	}{
		"no extra SANs": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1",
					Namespace: "default",
				},
			},
			expected: map[string]struct{}{
				"*.cluster1-mysql":                    {},
				"*.cluster1-mysql.default":            {},
				"*.cluster1-mysql.default.svc":        {},
				"cluster1-mysql-primary":              {},
				"cluster1-mysql-primary.default":      {},
				"cluster1-mysql-primary.default.svc":  {},
				"*.cluster1-orchestrator":             {},
				"*.cluster1-orchestrator.default":     {},
				"*.cluster1-orchestrator.default.svc": {},
				"*.cluster1-router":                   {},
				"*.cluster1-router.default":           {},
				"*.cluster1-router.default.svc":       {},
			},
		},
		"with extra SANs": {
			cr: &apiv1.PerconaServerMySQL{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster1",
					Namespace: "default",
				},
				Spec: apiv1.PerconaServerMySQLSpec{
					TLS: &apiv1.TLSSpec{
						SANs: []string{"extra.example.com"},
					},
				},
			},
			expected: map[string]struct{}{
				"*.cluster1-mysql":                    {},
				"*.cluster1-mysql.default":            {},
				"*.cluster1-mysql.default.svc":        {},
				"cluster1-mysql-primary":              {},
				"cluster1-mysql-primary.default":      {},
				"cluster1-mysql-primary.default.svc":  {},
				"*.cluster1-orchestrator":             {},
				"*.cluster1-orchestrator.default":     {},
				"*.cluster1-orchestrator.default.svc": {},
				"*.cluster1-router":                   {},
				"*.cluster1-router.default":           {},
				"*.cluster1-router.default.svc":       {},
				"extra.example.com":                   {},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			actual := make(map[string]struct{})
			for _, n := range DNSNames(tt.cr) {
				actual[n] = struct{}{}
			}
			assert.Equal(t, tt.expected, actual)
		})
	}
}
