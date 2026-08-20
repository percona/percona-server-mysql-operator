package binlogserver

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCipher(t *testing.T) {
	testCases := []struct {
		name        string
		wantKeySize int
		wantMode    string
		wantErr     bool
	}{
		{"AES-128-ECB", 16, "ECB", false},
		{"AES-192-CBC", 24, "CBC", false},
		{"AES-256-GCM", 32, "GCM", false},
		{"AES-256-CTR", 32, "CTR", false},
		{"AES-256-XTS", 32, "XTS", false},
		{"aes-128-ecb", 16, "ECB", false},
		{" AES-192-CBC ", 24, "CBC", false},
		{"DES-128-CBC", 0, "", true},
		{"AES-128", 0, "", true},
		{"xx", 0, "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gotKeySize, gotMode, err := ParseCipher(tc.name)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.wantKeySize, gotKeySize)
				require.Equal(t, tc.wantMode, gotMode)
			}
		})
	}
}

func TestValidateKeyring(t *testing.T) {
	testCases := []struct {
		desc    string
		keyring Keyring
		wantErr string
	}{
		{
			desc:    "no keys",
			keyring: Keyring{Version: 1, Keys: []Key{}},
			wantErr: "keyring must contain at least one key",
		},

		{
			desc: "key with no ID",
			keyring: Keyring{
				Version: 1,
				Keys: []Key{
					{Id: "", Cipher: "AES-256-CBC", DataHex: "00"},
				},
			},
			wantErr: "keyring contains a key with empty ID",
		},

		{
			desc: "key with no cipher",
			keyring: Keyring{
				Version: 1,
				Keys: []Key{
					{Id: "alpha", Cipher: "", DataHex: "00"},
				},
			},
			wantErr: "keyring contains a key with empty cipher",
		},

		{
			desc: "unsupported cipher",
			keyring: Keyring{
				Version: 1,
				Keys: []Key{
					{Id: "alpha", Cipher: "AES-256-xyz", DataHex: "00"},
				},
			},
			wantErr: "unsupported KEK cipher mode",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			err := tc.keyring.Validate()
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
