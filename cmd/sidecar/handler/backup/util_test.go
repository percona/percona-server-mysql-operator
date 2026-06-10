package backup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateBackupName(t *testing.T) {
	tests := map[string]struct {
		input   string
		wantErr bool
	}{
		"simple name":               {input: "my-backup", wantErr: false},
		"single character":          {input: "a", wantErr: false},
		"digits and dots":           {input: "backup-2025.01.15", wantErr: false},
		"max length 253":            {input: strings.Repeat("a", 253), wantErr: false},
		"empty":                     {input: "", wantErr: true},
		"too long":                  {input: strings.Repeat("a", 254), wantErr: true},
		"contains slash":            {input: "foo/bar", wantErr: true},
		"absolute path":             {input: "/etc/passwd", wantErr: true},
		"parent traversal":          {input: "../etc/passwd", wantErr: true},
		"backslash":                 {input: `foo\bar`, wantErr: true},
		"uppercase":                 {input: "MyBackup", wantErr: true},
		"leading hyphen":            {input: "-backup", wantErr: true},
		"trailing hyphen":           {input: "backup-", wantErr: true},
		"leading dot":               {input: ".backup", wantErr: true},
		"trailing dot":              {input: "backup.", wantErr: true},
		"underscore":                {input: "my_backup", wantErr: true},
		"space":                     {input: "my backup", wantErr: true},
		"null byte":                 {input: "backup\x00", wantErr: true},
		"only-dots traversal token": {input: "..", wantErr: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateBackupName(tc.input)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidBackupName)
				return
			}
			require.NoError(t, err)
		})
	}
}
