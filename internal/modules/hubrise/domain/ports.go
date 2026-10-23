package domain

import "time"

// Clock is the source of time. Production code uses a Europe/Brussels clock (infrastructure);
// tests inject a fake one so expiry, backoff and leases are deterministic.
type Clock interface {
	Now() time.Time
}

// TokenCodec seals the HubRise access token for storage. Implementations must be authenticated
// (a modified blob fails to open) and must carry a key version so keys can be rotated.
type TokenCodec interface {
	Encrypt(plain string) ([]byte, error)
	Decrypt(blob []byte) (string, error)
}
