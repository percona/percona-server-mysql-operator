package k8s

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type fakeConfigurable struct {
	configuration string
	resources     corev1.ResourceRequirements
	renderErr     error
}

func (c *fakeConfigurable) GetConfigMapName() string { return "cm" }

func (c *fakeConfigurable) GetConfigMapKey() string { return "my.cnf" }

func (c *fakeConfigurable) GetConfiguration() string { return c.configuration }

func (c *fakeConfigurable) GetResources() corev1.ResourceRequirements { return c.resources }

func (c *fakeConfigurable) ExecuteConfigurationTemplate(configuration string, memory *resource.Quantity) (string, error) {
	if c.renderErr != nil {
		return "", c.renderErr
	}
	return configuration + "\nrendered_with=" + memory.String(), nil
}

func TestRenderConfiguration(t *testing.T) {
	errRender := errors.New("boom")

	withMemory := func(limits, requests string) corev1.ResourceRequirements {
		res := corev1.ResourceRequirements{}
		if limits != "" {
			res.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(limits)}
		}
		if requests != "" {
			res.Requests = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse(requests)}
		}
		return res
	}

	tests := map[string]struct {
		configurable *fakeConfigurable
		want         string
		wantErr      error
		wantErrMsg   string
	}{
		"an empty configuration renders to nothing": {
			configurable: &fakeConfigurable{resources: withMemory("1Gi", "")},
		},
		"without memory the configuration is passed through verbatim": {
			configurable: &fakeConfigurable{configuration: "max_connections=250"},
			want:         "max_connections=250",
		},
		"the limit sizes the template": {
			configurable: &fakeConfigurable{configuration: "a={{ x }}", resources: withMemory("1Gi", "")},
			want:         "a={{ x }}\nrendered_with=1Gi",
		},
		"the request sizes the template when there is no limit": {
			configurable: &fakeConfigurable{configuration: "a={{ x }}", resources: withMemory("", "512Mi")},
			want:         "a={{ x }}\nrendered_with=512Mi",
		},
		"the limit wins over the request": {
			configurable: &fakeConfigurable{configuration: "a={{ x }}", resources: withMemory("2Gi", "512Mi")},
			want:         "a={{ x }}\nrendered_with=2Gi",
		},
		"a template with no memory to size it against is an error": {
			configurable: &fakeConfigurable{configuration: "a={{ containerMemoryLimit }}"},
			wantErrMsg:   "resources.limits[memory] or resources.requests[memory] should be specified for template usage in configuration",
		},
		"a failing template is reported": {
			configurable: &fakeConfigurable{configuration: "a={{ x }}", resources: withMemory("1Gi", ""), renderErr: errRender},
			wantErr:      errRender,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := RenderConfiguration(tc.configurable)

			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, got)
			case tc.wantErrMsg != "":
				require.EqualError(t, err, tc.wantErrMsg)
				assert.Empty(t, got)
			default:
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
