package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func logCollectorCR(crVersion string, spec *LogCollectorSpec) *PerconaServerMySQL {
	return &PerconaServerMySQL{
		Name: "cluster1",
		Spec: PerconaServerMySQLSpec{
			CRVersion:    crVersion,
			LogCollector: spec,
		},
	}
}

func TestLogCollectorEnabled(t *testing.T) {
	tests := map[string]struct {
		crVersion string
		spec      *LogCollectorSpec
		want      bool
	}{
		"enabled": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{Enabled: new(true)},
			want:      true,
		},
		"enabled on a newer cr version": {
			crVersion: "1.4.0",
			spec:      &LogCollectorSpec{Enabled: new(true)},
			want:      true,
		},
		"explicitly disabled": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{Enabled: new(false)},
		},
		"enabled unset": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{},
		},
		"spec absent": {
			crVersion: "1.3.0",
		},
		"cr version below 1.3.0": {
			crVersion: "1.2.0",
			spec:      &LogCollectorSpec{Enabled: new(true)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, logCollectorCR(tc.crVersion, tc.spec).LogCollectorEnabled())
		})
	}
}

func TestLogRotateExtraConfigMaps(t *testing.T) {
	tests := map[string]struct {
		spec *LogCollectorSpec
		want []string
	}{
		"extra config set": {
			spec: &LogCollectorSpec{
				LogRotate: &LogRotateSpec{ExtraConfig: corev1.LocalObjectReference{Name: "extra"}},
			},
			want: []string{"extra"},
		},
		"extra config name empty": {
			spec: &LogCollectorSpec{LogRotate: &LogRotateSpec{}},
		},
		"logrotate absent": {
			spec: &LogCollectorSpec{},
		},
		"spec absent": {},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, logCollectorCR("1.3.0", tc.spec).LogRotateExtraConfigMaps())
		})
	}
}

func TestLogRotateExtraConfigIndexerFunc(t *testing.T) {
	cr := logCollectorCR("1.3.0", &LogCollectorSpec{
		LogRotate: &LogRotateSpec{ExtraConfig: corev1.LocalObjectReference{Name: "extra"}},
	})

	assert.Equal(t, []string{"extra"}, LogRotateExtraConfigIndexerFunc(cr))
	assert.Nil(t, LogRotateExtraConfigIndexerFunc(&PerconaServerMySQLBackup{}))
}
