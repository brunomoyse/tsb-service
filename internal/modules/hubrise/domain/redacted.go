package domain

import (
	"encoding/json"
	"fmt"
)

// redactedMarker is what a Redacted value prints as, whatever the formatting verb.
const redactedMarker = "[REDACTED]"

// Redacted holds a secret (an access token, the client secret) and never shows it: it prints,
// logs and marshals as "[REDACTED]". The only way to the value is Reveal, which makes every use of
// the secret greppable.
type Redacted string

// Reveal returns the secret.
func (r Redacted) Reveal() string { return string(r) }

// String implements fmt.Stringer (zap.Any and zap.Stringer use it).
func (Redacted) String() string { return redactedMarker }

// GoString implements fmt.GoStringer, so %#v does not leak either.
func (Redacted) GoString() string { return redactedMarker }

// Format implements fmt.Formatter, covering every verb (%s, %v, %q, %x, ...).
func (Redacted) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redactedMarker)) }

// MarshalJSON implements json.Marshaler.
func (Redacted) MarshalJSON() ([]byte, error) { return json.Marshal(redactedMarker) }

// MarshalText implements encoding.TextMarshaler.
func (Redacted) MarshalText() ([]byte, error) { return []byte(redactedMarker), nil }
