package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
)

func TestDecryptingReader(t *testing.T) {
	tests := []struct {
		desc string
		// kekCipher is the cipher the file key is actually wrapped with.
		kekCipher string
		// keyringCipher is what the keyring advertises for the KEK. Empty means it matches kekCipher.
		keyringCipher string
		expectedError string
	}{
		{desc: "AES-128-ECB", kekCipher: "AES-128-ECB"},
		{desc: "AES-192-ECB", kekCipher: "AES-192-ECB"},
		{desc: "AES-256-ECB", kekCipher: "AES-256-ECB"},
		{desc: "AES-128-CBC", kekCipher: "AES-128-CBC"},
		{desc: "AES-192-CBC", kekCipher: "AES-192-CBC"},
		{desc: "AES-256-CBC", kekCipher: "AES-256-CBC"},
		{desc: "AES-128-GCM", kekCipher: "AES-128-GCM"},
		{desc: "AES-192-GCM", kekCipher: "AES-192-GCM"},
		{desc: "AES-256-GCM", kekCipher: "AES-256-GCM"},
		{
			desc:          "lowercase cipher name",
			kekCipher:     "AES-256-CBC",
			keyringCipher: "aes-256-cbc",
		},
		{
			desc:          "KEK size does not match the advertised cipher",
			kekCipher:     "AES-256-ECB",
			keyringCipher: "AES-128-ECB",
			expectedError: "KEK is 32 bytes, AES-128-ECB needs 16",
		},
		{
			desc:          "unsupported KEK cipher mode",
			kekCipher:     "AES-256-ECB",
			keyringCipher: "AES-256-XTS",
			expectedError: `unsupported KEK cipher mode "XTS"`,
		},
		{
			desc:          "malformed KEK cipher name",
			kekCipher:     "AES-256-ECB",
			keyringCipher: "AES-256",
			expectedError: `unsupported cipher "AES-256"`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			plaintext := append([]byte{}, binlogMagic...)
			plaintext = append(plaintext, []byte("events")...)

			ciphertext, entry, keyring := encryptBinlogForTest(t, plaintext, tc.kekCipher)
			if tc.keyringCipher != "" {
				keyring.Keys[0].Cipher = tc.keyringCipher
			}

			reader, err := decryptingReader(io.NopCloser(bytes.NewReader(ciphertext)), entry, keyring)
			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
				return
			}
			require.NoError(t, err)
			defer reader.Close() //nolint:errcheck

			got, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, plaintext, got)
		})
	}
}

// encryptBinlogForTest wraps a file key with kekCipher and encrypts plaintext with that file
// key using AES-256-CTR, the way the binlog server writes an encrypted binlog. The data cipher
// is always AES-256-CTR, so the file key is 32 bytes no matter how large the KEK is.
func encryptBinlogForTest(t *testing.T, plaintext []byte, kekCipher string) ([]byte, binlogserver.BinlogEntry, *binlogserver.Keyring) {
	t.Helper()

	keySize, mode, err := parseCipher(kekCipher)
	require.NoError(t, err)

	kek := bytes.Repeat([]byte{0x11}, keySize)
	fileKey := bytes.Repeat([]byte{0x22}, 32)

	keyEnv := wrapFileKeyForTest(t, kek, mode, fileKey)
	keyEnv.KekID = "alpha"

	dataIV := bytes.Repeat([]byte{0x33}, aes.BlockSize)
	dataBlock, err := aes.NewCipher(fileKey)
	require.NoError(t, err)
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCTR(dataBlock, dataIV).XORKeyStream(ciphertext, plaintext)

	entry := binlogserver.BinlogEntry{
		Name: "binlog.000001",
		Encryption: &binlogserver.Encryption{
			FileKeyEnvelope: keyEnv,
			FileDataEnvelope: &binlogserver.FileDataEnvelope{
				Cipher: "AES-256-CTR",
				IVHex:  hex.EncodeToString(dataIV),
			},
		},
	}

	keyring := &binlogserver.Keyring{
		Version: 1,
		Keys: []binlogserver.Key{
			{
				Id:      "alpha",
				Cipher:  kekCipher,
				DataHex: hex.EncodeToString(kek),
			},
		},
	}

	return ciphertext, entry, keyring
}

// wrapFileKeyForTest encrypts fileKey with kek in the given mode and returns the envelope the
// binlog server would publish for it.
func wrapFileKeyForTest(t *testing.T, kek []byte, mode string, fileKey []byte) *binlogserver.FileKeyEnvelope {
	t.Helper()

	block, err := aes.NewCipher(kek)
	require.NoError(t, err)

	switch mode {
	case "ECB":
		wrapped := make([]byte, len(fileKey))
		for i := 0; i < len(fileKey); i += block.BlockSize() {
			block.Encrypt(wrapped[i:i+block.BlockSize()], fileKey[i:i+block.BlockSize()])
		}
		return &binlogserver.FileKeyEnvelope{DataHex: hex.EncodeToString(wrapped)}

	case "CBC":
		iv := bytes.Repeat([]byte{0x44}, block.BlockSize())
		wrapped := make([]byte, len(fileKey))
		cipher.NewCBCEncrypter(block, iv).CryptBlocks(wrapped, fileKey)
		return &binlogserver.FileKeyEnvelope{
			DataHex: hex.EncodeToString(wrapped),
			IVHex:   hex.EncodeToString(iv),
		}

	case "GCM":
		iv := bytes.Repeat([]byte{0x44}, 12)
		aead, err := cipher.NewGCMWithNonceSize(block, len(iv))
		require.NoError(t, err)
		// Seal returns ciphertext||tag, the envelope carries them separately
		sealed := aead.Seal(nil, iv, fileKey, nil)
		return &binlogserver.FileKeyEnvelope{
			DataHex: hex.EncodeToString(sealed[:len(fileKey)]),
			IVHex:   hex.EncodeToString(iv),
			TagHex:  hex.EncodeToString(sealed[len(fileKey):]),
		}
	}

	t.Fatalf("test helper cannot wrap a file key in %q mode", mode)
	return nil
}
