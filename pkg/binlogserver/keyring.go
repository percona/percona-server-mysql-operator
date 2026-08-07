package binlogserver

type Keyring struct {
	Keys []Key `json:"keys"`
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
