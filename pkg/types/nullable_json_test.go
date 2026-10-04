package types

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNullableJSONScan(t *testing.T) {
	t.Run("SQL NULL becomes a nil value", func(t *testing.T) {
		nj := NullableJSON(`{"old":true}`)
		require.NoError(t, nj.Scan(nil))
		assert.Nil(t, nj, "a previous value is cleared")
		assert.True(t, nj.IsNull())
	})

	t.Run("bytes are copied, not aliased", func(t *testing.T) {
		src := []byte(`{"a":1}`)
		var nj NullableJSON
		require.NoError(t, nj.Scan(src))
		assert.Equal(t, `{"a":1}`, string(nj))
		src[2] = 'X'
		assert.Equal(t, `{"a":1}`, string(nj), "the driver may reuse its buffer")
	})

	t.Run("a string is accepted", func(t *testing.T) {
		var nj NullableJSON
		require.NoError(t, nj.Scan(`[1,2]`))
		assert.Equal(t, `[1,2]`, string(nj))
		assert.False(t, nj.IsNull())
	})

	t.Run("other types are refused", func(t *testing.T) {
		var nj NullableJSON
		for _, v := range []any{42, 1.5, true, struct{}{}} {
			require.ErrorContains(t, nj.Scan(v), "invalid type")
		}
	})
}

func TestNullableJSONValue(t *testing.T) {
	v, err := NullableJSON(nil).Value()
	require.NoError(t, err)
	assert.Nil(t, v, "a nil value is stored as SQL NULL")

	v, err = NullableJSON(`{"a":1}`).Value()
	require.NoError(t, err)
	assert.Equal(t, []byte(`{"a":1}`), v)

	v, err = NullableJSON{}.Value()
	require.NoError(t, err)
	assert.Equal(t, []byte{}, v, "an empty but non-nil value is not NULL")
}

func TestNullableJSONMarshalling(t *testing.T) {
	type holder struct {
		Extra NullableJSON `json:"extra"`
		Name  string       `json:"name"`
	}

	t.Run("a nil value marshals as null, a value is embedded as is", func(t *testing.T) {
		b, err := json.Marshal(holder{Name: "n"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"extra":null,"name":"n"}`, string(b))

		b, err = json.Marshal(holder{Extra: NullableJSON(`{"k":[1,2]}`), Name: "n"})
		require.NoError(t, err)
		assert.JSONEq(t, `{"extra":{"k":[1,2]},"name":"n"}`, string(b))
	})

	t.Run("null unmarshals to nil, anything else is kept verbatim", func(t *testing.T) {
		var h holder
		require.NoError(t, json.Unmarshal([]byte(`{"extra":null,"name":"n"}`), &h))
		assert.True(t, h.Extra.IsNull())

		require.NoError(t, json.Unmarshal([]byte(`{"extra":{"k":"v"},"name":"n"}`), &h))
		assert.JSONEq(t, `{"k":"v"}`, string(h.Extra))
		assert.False(t, h.Extra.IsNull())
	})

	t.Run("a round trip preserves the document", func(t *testing.T) {
		in := `{"extra":[{"name":"chopsticks","quantity":2}],"name":"x"}`
		var h holder
		require.NoError(t, json.Unmarshal([]byte(in), &h))
		out, err := json.Marshal(h)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(out))
	})
}

func TestNullableJSONUnmarshalInto(t *testing.T) {
	type extra struct {
		Name     string `json:"name"`
		Quantity int    `json:"quantity"`
	}
	var got []extra
	require.NoError(t, NullableJSON(`[{"name":"sauce","quantity":3}]`).Unmarshal(&got))
	assert.Equal(t, []extra{{"sauce", 3}}, got)

	got = []extra{{"kept", 1}}
	require.NoError(t, NullableJSON(nil).Unmarshal(&got), "NULL leaves the target alone")
	assert.Equal(t, []extra{{"kept", 1}}, got)

	require.Error(t, NullableJSON(`{`).Unmarshal(&got))
}
