package v1

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCheckNSetDefaultsAutoConfig(t *testing.T) {
	tests := map[string]struct {
		enabled      *bool
		loadType     AutoConfigLoadType
		wantEnabled  bool
		wantLoadType AutoConfigLoadType
	}{
		"unset stays disabled": {
			wantEnabled:  false,
			wantLoadType: "",
		},
		"explicitly disabled keeps loadType unset": {
			enabled:      new(false),
			wantEnabled:  false,
			wantLoadType: "",
		},
		"explicitly enabled without loadType gets default": {
			enabled:      new(true),
			wantEnabled:  true,
			wantLoadType: AutoConfigLoadTypeSomeWrites,
		},
		"explicitly enabled with custom loadType is preserved": {
			enabled:      new(true),
			loadType:     AutoConfigLoadTypeHeavyWrites,
			wantEnabled:  true,
			wantLoadType: AutoConfigLoadTypeHeavyWrites,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cr := new(PerconaServerMySQL)
			cr.Spec.MySQL.AutoConfig.Enabled = tc.enabled
			cr.Spec.MySQL.AutoConfig.LoadType = tc.loadType

			// CheckNSetDefaults returns an error later (nil volumeSpec), but the
			// autoconfig defaulting runs before that, so the fields are set
			// regardless.
			_ = cr.CheckNSetDefaults(t.Context(), nil)

			assert.Equal(t, tc.wantEnabled, cr.Spec.MySQL.AutoConfig.IsEnabled())
			assert.Equal(t, tc.wantLoadType, cr.Spec.MySQL.AutoConfig.LoadType)
		})
	}
}
