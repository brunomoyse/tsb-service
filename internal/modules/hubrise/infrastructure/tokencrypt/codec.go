// Package tokencrypt seals the HubRise access token at rest with AES-256-GCM.
//
// A sealed blob is: 1 key version byte, a 12 byte random nonce, then the ciphertext with its GCM
// tag. The version byte is also bound as additional authenticated data, so it cannot be changed
// without failing authentication. The version lets a new key be introduced while blobs sealed with
// the old one are still readable (a Codec holds several keys and seals with the current one).
package tokencrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// KeySize is the size of an AES-256 key in bytes.
	KeySize = 32
	// CurrentVersion is the key version used when a single key is configured.
	CurrentVersion byte = 1

	nonceSize = 12
)

var (
	// ErrUnknownKeyVersion means the blob was sealed with a key this codec does not hold.
	ErrUnknownKeyVersion = errors.New("tokencrypt: unknown key version")
	// ErrMalformed means the blob is too short to be a sealed token.
	ErrMalformed = errors.New("tokencrypt: malformed ciphertext")
	// ErrDecrypt means authentication failed: wrong key or a modified blob.
	ErrDecrypt = errors.New("tokencrypt: cannot decrypt (wrong key or tampered data)")
)

// Codec seals and opens tokens. It is safe for concurrent use.
type Codec struct {
	aeads   map[byte]cipher.AEAD
	current byte
	rand    io.Reader
}

// New returns a Codec with a single key, version CurrentVersion.
func New(key []byte) (*Codec, error) {
	return NewKeyring(CurrentVersion, map[byte][]byte{CurrentVersion: key})
}

// NewKeyring returns a Codec that seals with the key of version current and opens blobs of any
// version in keys.
func NewKeyring(current byte, keys map[byte][]byte) (*Codec, error) {
	if _, ok := keys[current]; !ok {
		return nil, fmt.Errorf("tokencrypt: no key for current version %d", current)
	}
	aeads := make(map[byte]cipher.AEAD, len(keys))
	for version, key := range keys {
		if len(key) != KeySize {
			return nil, fmt.Errorf("tokencrypt: key version %d is %d bytes, want %d", version, len(key), KeySize)
		}
		// aes.NewCipher only fails on a key size other than 16/24/32 and cipher.NewGCM never fails
		// for AES: the size was checked above, so neither error can happen.
		block, _ := aes.NewCipher(key)
		aead, _ := cipher.NewGCM(block)
		aeads[version] = aead
	}
	return &Codec{aeads: aeads, current: current, rand: rand.Reader}, nil
}

// ParseKey decodes a base64 (standard or URL alphabet, padded or not) 32 byte key, as found in
// HUBRISE_TOKEN_KEY.
func ParseKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	var key []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err = enc.DecodeString(s); err == nil {
			break
		}
	}
	if err != nil {
		return nil, errors.New("tokencrypt: key is not valid base64")
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("tokencrypt: key is %d bytes, want %d", len(key), KeySize)
	}
	return key, nil
}

// Encrypt seals plain with the current key.
func (c *Codec) Encrypt(plain string) ([]byte, error) {
	aead := c.aeads[c.current]
	out := make([]byte, 1+nonceSize, 1+nonceSize+len(plain)+aead.Overhead())
	out[0] = c.current
	if _, err := io.ReadFull(c.rand, out[1:1+nonceSize]); err != nil {
		return nil, fmt.Errorf("tokencrypt: nonce: %w", err)
	}
	return aead.Seal(out, out[1:1+nonceSize], []byte(plain), out[:1]), nil
}

// Decrypt opens a blob sealed by Encrypt with any key of the keyring.
func (c *Codec) Decrypt(blob []byte) (string, error) {
	if len(blob) < 1+nonceSize {
		return "", ErrMalformed
	}
	aead, ok := c.aeads[blob[0]]
	if !ok {
		return "", ErrUnknownKeyVersion
	}
	if len(blob) < 1+nonceSize+aead.Overhead() {
		return "", ErrMalformed
	}
	plain, err := aead.Open(nil, blob[1:1+nonceSize], blob[1+nonceSize:], blob[:1])
	if err != nil {
		return "", ErrDecrypt
	}
	return string(plain), nil
}

// NeedsRotation reports whether blob was sealed with an older key than the current one, so the
// caller can re-seal it.
func (c *Codec) NeedsRotation(blob []byte) bool {
	return len(blob) > 0 && blob[0] != c.current
}
