package destination

import (
	"context"
	"crypto/sha256"
	"os"
)

// secretKeyCipher is the inventory-service Cipher implementation. Unlike treasury
// (which has a dedicated gateways.KeyProvider), inventory has no key-management
// subsystem, so the AES-256 key is derived deterministically from the SECRET_KEY
// environment variable via SHA-256. SHA-256 always yields exactly 32 bytes, which
// is the AES-256 key size, regardless of the SECRET_KEY length.
type secretKeyCipher struct {
	key [32]byte
}

// NewSecretKeyCipher builds a Cipher whose key is sha256([]byte(SECRET_KEY)). It
// reads SECRET_KEY at construction time. The derived 32-byte key is used as both
// the primary (encrypt) key and the sole candidate (decrypt) key.
func NewSecretKeyCipher() Cipher {
	return &secretKeyCipher{key: sha256.Sum256([]byte(os.Getenv("SECRET_KEY")))}
}

// PrimaryKey returns the 32-byte SHA-256(SECRET_KEY) key used to encrypt new data.
func (c *secretKeyCipher) PrimaryKey(context.Context) []byte {
	k := c.key
	return k[:]
}

// CandidateKeys returns the single derived key. Inventory does not rotate this
// key, so there is exactly one candidate.
func (c *secretKeyCipher) CandidateKeys(context.Context) [][]byte {
	k := c.key
	return [][]byte{k[:]}
}
