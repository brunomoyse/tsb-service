package tokencrypt

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, KeySize) }

func TestRoundTrip(t *testing.T) {
	c, err := New(key(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"tok_abc123", "", "é汉字 with spaces", strings.Repeat("x", 4096)} {
		blob, err := c.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}
		if blob[0] != CurrentVersion {
			t.Errorf("version byte = %d, want %d", blob[0], CurrentVersion)
		}
		if plain != "" && bytes.Contains(blob, []byte(plain)) {
			t.Error("the blob contains the plain text")
		}
		got, err := c.Decrypt(blob)
		if err != nil || got != plain {
			t.Errorf("Decrypt = %q, %v; want %q", got, err, plain)
		}
	}
}

func TestEncryptIsRandomised(t *testing.T) {
	c, _ := New(key(1))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if bytes.Equal(a, b) {
		t.Error("two seals of the same token are identical: the nonce is not random")
	}
}

func TestWrongKey(t *testing.T) {
	c1, _ := New(key(1))
	c2, _ := New(key(2))
	blob, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(blob); !errors.Is(err, ErrDecrypt) {
		t.Errorf("err = %v, want ErrDecrypt", err)
	}
}

func TestTamper(t *testing.T) {
	c, _ := New(key(1))
	blob, _ := c.Encrypt("secret")
	for i := 1; i < len(blob); i++ { // every byte after the version, nonce and ciphertext included
		bad := bytes.Clone(blob)
		bad[i] ^= 0x01
		if _, err := c.Decrypt(bad); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("flipping byte %d: err = %v, want ErrDecrypt", i, err)
		}
	}
}

func TestMalformed(t *testing.T) {
	c, _ := New(key(1))
	for _, blob := range [][]byte{nil, {}, {1}, bytes.Repeat([]byte{1}, nonceSize), append([]byte{1}, make([]byte, nonceSize+3)...)} {
		if _, err := c.Decrypt(blob); !errors.Is(err, ErrMalformed) {
			t.Errorf("Decrypt(%d bytes) err = %v, want ErrMalformed", len(blob), err)
		}
	}
}

func TestKeyVersions(t *testing.T) {
	old, _ := New(key(1))
	oldBlob, _ := old.Encrypt("old token")

	ring, err := NewKeyring(2, map[byte][]byte{1: key(1), 2: key(2)})
	if err != nil {
		t.Fatal(err)
	}
	// A blob sealed with the retired key is still readable and flagged for rotation.
	got, err := ring.Decrypt(oldBlob)
	if err != nil || got != "old token" {
		t.Fatalf("Decrypt(old) = %q, %v", got, err)
	}
	if !ring.NeedsRotation(oldBlob) {
		t.Error("an old blob must need rotation")
	}
	// New seals use the current version, which the single-key codec does not know.
	newBlob, _ := ring.Encrypt("new token")
	if newBlob[0] != 2 || ring.NeedsRotation(newBlob) {
		t.Errorf("new blob version = %d, rotation = %v", newBlob[0], ring.NeedsRotation(newBlob))
	}
	if _, err := old.Decrypt(newBlob); !errors.Is(err, ErrUnknownKeyVersion) {
		t.Errorf("err = %v, want ErrUnknownKeyVersion", err)
	}
	if ring.NeedsRotation(nil) {
		t.Error("an empty blob needs no rotation")
	}
	// The version byte is authenticated: relabelling a v1 blob as v2 must not open it.
	relabelled := bytes.Clone(oldBlob)
	relabelled[0] = 2
	if _, err := ring.Decrypt(relabelled); !errors.Is(err, ErrDecrypt) {
		t.Errorf("relabelled blob: err = %v, want ErrDecrypt", err)
	}
}

func TestNewKeyringErrors(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Error("a 16 byte key must be refused")
	}
	if _, err := NewKeyring(3, map[byte][]byte{1: key(1)}); err == nil {
		t.Error("a missing current key must be refused")
	}
}

func TestParseKey(t *testing.T) {
	raw := key(7)
	for name, s := range map[string]string{
		"std":        base64.StdEncoding.EncodeToString(raw),
		"raw std":    base64.RawStdEncoding.EncodeToString(raw),
		"url":        base64.URLEncoding.EncodeToString(raw),
		"raw url":    base64.RawURLEncoding.EncodeToString(raw),
		"whitespace": "  " + base64.StdEncoding.EncodeToString(raw) + "\n",
	} {
		got, err := ParseKey(s)
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("%s: ParseKey = %v, %v", name, got, err)
		}
	}
	if _, err := ParseKey("not base64 !!"); err == nil {
		t.Error("garbage must be refused")
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(make([]byte, 31))); err == nil {
		t.Error("a 31 byte key must be refused")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestEncryptRandomFailure(t *testing.T) {
	c, _ := New(key(1))
	c.rand = failingReader{}
	if _, err := c.Encrypt("x"); err == nil {
		t.Error("a failing random source must surface")
	}
}
