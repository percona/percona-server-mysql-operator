package mysql

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatConfigValue(t *testing.T) {
	tests := map[string]struct {
		value string
		want  string
	}{
		"passes integers through unquoted":       {value: "100", want: "100"},
		"passes floats through unquoted":         {value: "0.5", want: "0.5"},
		"passes negative numbers through":        {value: "-1", want: "-1"},
		"quotes strings":                         {value: "utf8mb4", want: "'utf8mb4'"},
		"quotes enum lists":                      {value: "STRICT_TRANS_TABLES,NO_ZERO_DATE", want: "'STRICT_TRANS_TABLES,NO_ZERO_DATE'"},
		"quotes an empty value":                  {value: "", want: "''"},
		"trims surrounding whitespace":           {value: "  100  ", want: "100"},
		"expands a kilobyte suffix":              {value: "16K", want: "16384"},
		"expands a megabyte suffix":              {value: "64M", want: "67108864"},
		"expands a gigabyte suffix":              {value: "1G", want: "1073741824"},
		"expands a terabyte suffix":              {value: "2T", want: "2199023255552"},
		"expands a lowercase suffix":             {value: "1g", want: "1073741824"},
		"reads a lowercase m as mega, not milli": {value: "100m", want: "104857600"},
		// Quantity saturates at MaxInt64 rather than reporting overflow, and it
		// accepts fractions that mysqld itself would reject. Both are recorded
		// here as known divergences, not as desired behaviour.
		"saturates an overflowing suffix":        {value: "9223372036854775807T", want: "9223372036854775807"},
		"accepts a suffixed non-integer":         {value: "1.5G", want: "1610612736"},
		"quotes a bare suffix":                   {value: "G", want: "'G'"},
		"quotes a word ending in a suffix":       {value: "PAGE", want: "'PAGE'"},
		"maps a bare boolean key to ON":          {value: "true", want: "ON"},
		"maps an explicit false to OFF":          {value: "false", want: "OFF"},
		"normalises on to uppercase":             {value: "on", want: "ON"},
		"normalises off to uppercase":            {value: "off", want: "OFF"},
		"leaves an enum resembling a bool alone": {value: "ON_PERMISSIVE", want: "'ON_PERMISSIVE'"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, FormatConfigValue(tt.value))
		})
	}
}
