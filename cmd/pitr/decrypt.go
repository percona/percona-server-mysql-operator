package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/percona/percona-server-mysql-operator/pkg/binlogserver"
)

// this is the 4 byte header that every mysqlbinlog starts with.
// we use it to assert that the decrypted stream is valid and does not contain any garbage.
var binlogMagic = []byte{0xfe, 'b', 'i', 'n'}

func decryptingReader(
	r io.ReadCloser,
	entry binlogserver.BinlogEntry,
	keyring *binlogserver.Keyring,
) (io.ReadCloser, error) {
	if entry.Encryption == nil {
		return r, nil
	}

	keyEnv := entry.Encryption.FileKeyEnvelope
	dataEnv := entry.Encryption.FileDataEnvelope
	if keyEnv == nil || dataEnv == nil {
		return nil, fmt.Errorf("binlog %s: incomplete encryption metadata", entry.Name)
	}

	if keyring == nil {
		return nil, fmt.Errorf("binlog %s is encrypted with KEK %q but no keyring is available: KEYRING_PATH is not set", entry.Name, keyEnv.KekID)
	}

	kek := keyring.FindKey(keyEnv.KekID)
	if kek == nil {
		return nil, fmt.Errorf("binlog %s is encrypted with KEK %q but it is not in the keyring", entry.Name, keyEnv.KekID)
	}

	fileKey, err := unwrapFileKey(keyEnv, *kek)
	if err != nil {
		return nil, fmt.Errorf("binlog %s: unwrap file key with KEK %q: %w", entry.Name, kek.Id, err)
	}

	stream, err := dataStream(dataEnv, fileKey)
	if err != nil {
		return nil, fmt.Errorf("binlog %s: %w", entry.Name, err)
	}

	decrypted := io.Reader(&cipher.StreamReader{S: stream, R: r})

	// check that the decrypted stream is valid (does not contain garbage)
	magic := make([]byte, len(binlogMagic))
	if _, err := io.ReadFull(decrypted, magic); err != nil {
		return nil, fmt.Errorf("binlog %s: read header: %w", entry.Name, err)
	}
	if !bytes.Equal(magic, binlogMagic) {
		return nil, fmt.Errorf(
			"binlog %s: decrypted data does not start with the binlog magic header, KEK %q is likely not the key this file was encrypted with",
			entry.Name, kek.Id,
		)
	}

	return decryptedReadCloser{
		// the header was consumed to check it, so put it back in front of the stream
		Reader: io.MultiReader(bytes.NewReader(magic), decrypted),
		Closer: r,
	}, nil
}

type decryptedReadCloser struct {
	io.Reader
	io.Closer
}

func unwrapFileKey(env *binlogserver.FileKeyEnvelope, kek binlogserver.Key) ([]byte, error) {
	keySize, mode, err := binlogserver.ParseCipher(kek.Cipher)
	if err != nil {
		return nil, err
	}

	kekBytes, err := hex.DecodeString(kek.DataHex)
	if err != nil {
		return nil, fmt.Errorf("decode KEK: %w", err)
	}
	if len(kekBytes) != keySize {
		return nil, fmt.Errorf("KEK is %d bytes, %s needs %d", len(kekBytes), kek.Cipher, keySize)
	}

	block, err := aes.NewCipher(kekBytes)
	if err != nil {
		return nil, fmt.Errorf("create KEK cipher: %w", err)
	}

	wrapped, err := hex.DecodeString(env.DataHex)
	if err != nil {
		return nil, fmt.Errorf("decode wrapped file key: %w", err)
	}
	if len(wrapped) == 0 {
		return nil, errors.New("wrapped file key is empty")
	}

	blockSize := block.BlockSize()

	switch mode {
	case "ECB":
		if len(wrapped)%blockSize != 0 {
			return nil, fmt.Errorf("wrapped file key is %d bytes, not a multiple of the %d-byte block size", len(wrapped), blockSize)
		}
		fileKey := make([]byte, len(wrapped))
		for i := 0; i < len(wrapped); i += blockSize {
			block.Decrypt(fileKey[i:i+blockSize], wrapped[i:i+blockSize])
		}
		return fileKey, nil

	case "CBC":
		iv, err := hex.DecodeString(env.IVHex)
		if err != nil {
			return nil, fmt.Errorf("decode file key IV: %w", err)
		}
		if len(iv) != blockSize {
			return nil, fmt.Errorf("file key IV is %d bytes, %s needs %d", len(iv), kek.Cipher, blockSize)
		}
		if len(wrapped)%blockSize != 0 {
			return nil, fmt.Errorf("wrapped file key is %d bytes, not a multiple of the %d-byte block size", len(wrapped), blockSize)
		}
		fileKey := make([]byte, len(wrapped))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(fileKey, wrapped)
		return fileKey, nil

	case "CTR":
		iv, err := hex.DecodeString(env.IVHex)
		if err != nil {
			return nil, fmt.Errorf("decode file key IV: %w", err)
		}
		if len(iv) != blockSize {
			return nil, fmt.Errorf("file key IV is %d bytes, %s needs %d", len(iv), kek.Cipher, blockSize)
		}
		fileKey := make([]byte, len(wrapped))
		cipher.NewCTR(block, iv).XORKeyStream(fileKey, wrapped)
		return fileKey, nil

	case "GCM":
		iv, err := hex.DecodeString(env.IVHex)
		if err != nil {
			return nil, fmt.Errorf("decode file key IV: %w", err)
		}
		if len(iv) == 0 {
			return nil, errors.New("file key IV is required for GCM")
		}
		tag, err := hex.DecodeString(env.TagHex)
		if err != nil {
			return nil, fmt.Errorf("decode file key tag: %w", err)
		}
		aead, err := cipher.NewGCMWithNonceSize(block, len(iv))
		if err != nil {
			return nil, fmt.Errorf("create GCM: %w", err)
		}
		if len(tag) != aead.Overhead() {
			return nil, fmt.Errorf("file key tag is %d bytes, %s needs %d", len(tag), kek.Cipher, aead.Overhead())
		}
		sealed := make([]byte, 0, len(wrapped)+len(tag))
		sealed = append(sealed, wrapped...)
		sealed = append(sealed, tag...)
		fileKey, err := aead.Open(nil, iv, sealed, nil)
		if err != nil {
			return nil, fmt.Errorf("authenticate file key: %w", err)
		}
		return fileKey, nil
	}

	return nil, fmt.Errorf("unsupported KEK cipher mode %q", mode)
}

func dataStream(env *binlogserver.FileDataEnvelope, fileKey []byte) (cipher.Stream, error) {
	keySize, mode, err := binlogserver.ParseCipher(env.Cipher)
	if err != nil {
		return nil, err
	}
	if mode != "CTR" {
		return nil, fmt.Errorf("unsupported data cipher mode %q, only CTR is supported", mode)
	}
	if len(fileKey) != keySize {
		return nil, fmt.Errorf("file key is %d bytes, %s needs %d", len(fileKey), env.Cipher, keySize)
	}

	block, err := aes.NewCipher(fileKey)
	if err != nil {
		return nil, fmt.Errorf("create data cipher: %w", err)
	}

	iv, err := hex.DecodeString(env.IVHex)
	if err != nil {
		return nil, fmt.Errorf("decode data IV: %w", err)
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("data IV is %d bytes, %s needs %d", len(iv), env.Cipher, block.BlockSize())
	}

	return cipher.NewCTR(block, iv), nil
}
