package binlogserver

import "github.com/pkg/errors"

type Keyring struct {
	Version int   `json:"version"`
	Keys    []Key `json:"keys"`
}

type Key struct {
	Id      string `json:"id"`
	Cipher  string `json:"cipher"`
	DataHex string `json:"data_hex"`
}

func (k Keyring) FindKey(id string) *Key {
	for _, key := range k.Keys {
		if key.Id == id {
			return &key
		}
	}
	return nil
}

func (k Keyring) Validate() error {
	if len(k.Keys) == 0 {
		return errors.New("keyring must contain at least one key")
	}
	return nil
}
