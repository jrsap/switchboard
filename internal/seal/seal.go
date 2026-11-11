// Package seal implements the switchboard key hierarchy and payload
// encryption with XChaCha20-Poly1305 (golang.org/x/crypto):
//
//   - a root key, held only by the router process;
//   - per-org data keys, stored at rest only wrapped (sealed) with the root
//     key, e.g. in the org DB, and handed to the org child unwrapped over
//     the loopback spawn handshake;
//   - request/response bodies, encrypted per org with the org data key
//     before they reach storage.
//
// Wrapped/encrypted values are base64(nonce || ciphertext || tag).
package seal

import (
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the size in bytes of root and per-org data keys.
const KeySize = chacha20poly1305.KeySize // 32

var ErrBadPayload = errors.New("seal: malformed payload")

// NewKey returns a fresh random 32-byte key.
func NewKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("seal: key must be %d bytes, got %d", KeySize, len(key))
	}
	return chacha20poly1305.NewX(key)
}

// Wrap seals dataKey with rootKey and returns base64(nonce || ct).
func Wrap(rootKey, dataKey []byte) (string, error) {
	a, err := aead(rootKey)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, dataKey, nil)), nil
}

// Unwrap reverses Wrap.
func Unwrap(rootKey []byte, wrapped string) ([]byte, error) {
	a, err := aead(rootKey)
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, fmt.Errorf("%w: bad base64", ErrBadPayload)
	}
	ns := a.NonceSize()
	if len(raw) < ns+a.Overhead() {
		return nil, ErrBadPayload
	}
	plain, err := a.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("seal: open: %w", err)
	}
	return plain, nil
}

// Encrypt seals plaintext with key (an org data key), returning
// base64(nonce || ct). Used for request/response bodies before storage.
func Encrypt(key, plaintext []byte) (string, error) {
	return Wrap(key, plaintext)
}

// Decrypt reverses Encrypt.
func Decrypt(key []byte, payload string) ([]byte, error) {
	return Unwrap(key, payload)
}
