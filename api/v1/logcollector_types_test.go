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

func TestValidateLogCollector(t *testing.T) {
	tests := map[string]struct {
		crVersion  string
		spec       *LogCollectorSpec
		sidecars   []corev1.Container
		wantErrMsg string
	}{
		"no log collector": {
			crVersion: "1.3.0",
		},
		"valid schedule": {
			crVersion: "1.3.0",
			spec: &LogCollectorSpec{
				LogRotate: &LogRotateSpec{Schedule: "30 3 * * *"},
			},
		},
		"empty schedule": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{LogRotate: &LogRotateSpec{}},
		},
		"unparsable schedule": {
			crVersion: "1.3.0",
			spec: &LogCollectorSpec{
				LogRotate: &LogRotateSpec{Schedule: "every night"},
			},
			wantErrMsg: "invalid logcollector.logRotate.schedule: expected exactly 5 fields, found 2: [every night]",
		},
		"schedule with a newline": {
			crVersion: "1.3.0",
			spec: &LogCollectorSpec{
				LogRotate: &LogRotateSpec{Schedule: "0 0 * * *\n* * * * * rm -rf /"},
			},
			wantErrMsg: "logcollector.logRotate.schedule can't contain newlines",
		},
		"schedule is validated even when disabled": {
			crVersion: "1.3.0",
			spec: &LogCollectorSpec{
				Enabled:   new(false),
				LogRotate: &LogRotateSpec{Schedule: "every night"},
			},
			wantErrMsg: "invalid logcollector.logRotate.schedule: expected exactly 5 fields, found 2: [every night]",
		},
		"sidecar named logs": {
			crVersion:  "1.3.0",
			spec:       &LogCollectorSpec{Enabled: new(true)},
			sidecars:   []corev1.Container{{Name: LogCollectorContainerName}},
			wantErrMsg: "mysql.sidecars can't use the container name logs, it's reserved by the log collector",
		},
		"sidecar named logrotate": {
			crVersion:  "1.3.0",
			spec:       &LogCollectorSpec{Enabled: new(true)},
			sidecars:   []corev1.Container{{Name: LogRotateContainerName}},
			wantErrMsg: "mysql.sidecars can't use the container name logrotate, it's reserved by the log collector",
		},
		"sidecar name is reserved while enabled is unset": {
			crVersion:  "1.3.0",
			spec:       new(LogCollectorSpec),
			sidecars:   []corev1.Container{{Name: LogCollectorContainerName}},
			wantErrMsg: "mysql.sidecars can't use the container name logs, it's reserved by the log collector",
		},
		"sidecar name is free when the collector is off": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{Enabled: new(false)},
			sidecars:  []corev1.Container{{Name: LogCollectorContainerName}},
		},
		"sidecar name is reserved with an unset cr version": {
			spec:       &LogCollectorSpec{Enabled: new(true)},
			sidecars:   []corev1.Container{{Name: LogCollectorContainerName}},
			wantErrMsg: "mysql.sidecars can't use the container name logs, it's reserved by the log collector",
		},
		"sidecar name is free below cr version 1.3.0": {
			crVersion: "1.2.0",
			spec:      &LogCollectorSpec{Enabled: new(true)},
			sidecars:  []corev1.Container{{Name: LogCollectorContainerName}},
		},
		"unrelated sidecar": {
			crVersion: "1.3.0",
			spec:      &LogCollectorSpec{Enabled: new(true)},
			sidecars:  []corev1.Container{{Name: "user-sidecar"}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := logCollectorCR(tc.crVersion, tc.spec)
			cr.Spec.MySQL.Sidecars = tc.sidecars

			err := cr.validateLogCollector()

			if tc.wantErrMsg != "" {
				assert.EqualError(t, err, tc.wantErrMsg)
				return
			}
			assert.NoError(t, err)
		})
	}
}
