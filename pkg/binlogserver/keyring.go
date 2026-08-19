package binlogserver

import (
	"slices"
	"strings"

	"github.com/pkg/errors"
)

var kekCipherModes = []string{"ECB", "CBC", "CTR", "GCM"}

type Keyring struct {
	Version int   `json:"version"`
	Keys    []Key `json:"keys"`
}

type Key struct {
	Id      string `json:"id"`
	Cipher  string `json:"cipher"`
	DataHex string `json:"data_hex"`
}

// DecodeKeyring parses keyring JSON and validates its contents. It rejects
// input that decodes without error but yields an unusable keyring, e.g. the
// JSON literal "null" or an object with no keys.
func DecodeKeyring(data []byte) (Keyring, error) {
	keyring, err := decodeKeyringStrict(data)
	if err != nil {
		return Keyring{}, err
	}

	if err := keyring.Validate(); err != nil {
		return Keyring{}, errors.Wrap(err, "validate keyring")
	}

	return keyring, nil
}

func (k Keyring) FindKey(id string) *Key {
	for i, key := range k.Keys {
		if key.Id == id {
			return &k.Keys[i]
		}
	}
	return nil
}

func (k Keyring) Validate() error {
	if len(k.Keys) == 0 {
		return errors.New("keyring must contain at least one key")
	}

	for _, key := range k.Keys {
		if key.Id == "" {
			return errors.New("keyring contains a key with empty ID")
		}
		if key.Cipher == "" {
			return errors.New("keyring contains a key with empty cipher")
		}

		_, mode, err := ParseCipher(key.Cipher)
		if err != nil {
			return errors.Wrapf(err, "keyring key %q", key.Id)
		}
		if !slices.Contains(kekCipherModes, mode) {
			return errors.Errorf("keyring key %q: unsupported KEK cipher mode %q, supported modes are %s",
				key.Id, mode, strings.Join(kekCipherModes, ", "))
		}
	}

	return nil
}

func ParseCipher(name string) (keySize int, mode string, err error) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(name)), "-")
	if len(parts) != 3 || parts[0] != "AES" {
		return 0, "", errors.Errorf("unsupported cipher %q, expected AES-<128|192|256>-<mode>", name)
	}

	switch parts[1] {
	case "128":
		keySize = 16
	case "192":
		keySize = 24
	case "256":
		keySize = 32
	default:
		return 0, "", errors.Errorf("unsupported AES key size in cipher %q", name)
	}

	return keySize, parts[2], nil
}
