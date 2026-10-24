package application

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The catalog publisher works on generic JSON, not on the typed wire structs, because a PUT clears
// and recreates the whole HubRise catalog: every key the shop does not model (deals, discounts,
// charges, price overrides, restrictions, custom fields of other systems, keys HubRise adds later)
// must come back out exactly as it went in. Numbers are decoded as json.Number, so they are never
// rewritten by a float round trip.

type jobj = map[string]any

// decodeJSON decodes raw into generic values with numbers kept as json.Number.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// decodeObject decodes a JSON object; an empty input is an empty object.
func decodeObject(raw []byte) (jobj, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return jobj{}, nil
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return nil, err
	}
	o, ok := v.(jobj)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object, got %T", v)
	}
	return o, nil
}

// marshalJSON encodes v without HTML escaping (names such as "Soupe & riz" stay readable and the
// bytes are stable). Map keys come out sorted. Only generic values (maps, slices, strings, numbers,
// booleans) go in, which always encode.
func marshalJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// cloneJSON deep copies a generic value.
func cloneJSON(v any) any {
	switch x := v.(type) {
	case jobj:
		out := make(jobj, len(x))
		for k, e := range x {
			out[k] = cloneJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneJSON(e)
		}
		return out
	}
	return v
}

func cloneObj(o jobj) jobj {
	if o == nil {
		return jobj{}
	}
	return cloneJSON(o).(jobj)
}

// jstr reads a string value ("" for anything else, a number is read as its text).
func jstr(o jobj, key string) string {
	switch v := o[key].(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	}
	return ""
}

// jobjs reads an array of objects (anything else is skipped).
func jobjs(o jobj, key string) []jobj {
	list, _ := o[key].([]any)
	out := make([]jobj, 0, len(list))
	for _, e := range list {
		if m, ok := e.(jobj); ok {
			out = append(out, m)
		}
	}
	return out
}

// jstrs reads an array of strings.
func jstrs(o jobj, key string) []string {
	list, _ := o[key].([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func objsToAny(in []jobj) []any {
	out := make([]any, len(in))
	for i, o := range in {
		out[i] = o
	}
	return out
}

// jint reads an integer value.
func jint(o jobj, key string) (int, bool) {
	n, ok := o[key].(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	if err != nil {
		return 0, false
	}
	return int(i), true
}

// dropNulls removes the keys whose value is JSON null (HubRise answers null for an absent optional
// field and treats it as absent on upload).
func dropNulls(o jobj) {
	for k, v := range o {
		if v == nil {
			delete(o, k)
		}
	}
}

// canonicalJSON is the comparison form of a generic value: keys sorted (the encoder does that), ids
// removed everywhere (ids may change after every PUT). The alternative spellings of the documentation
// are folded by parseBaseCatalog before. Two catalogs with the same canonical form are the same catalog.
func canonicalJSON(v any) []byte {
	return marshalJSON(canonicalize(v))
}

func canonicalize(v any) any {
	switch x := v.(type) {
	case jobj:
		out := make(jobj, len(x))
		for k, e := range x {
			if k == "id" || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "_ids") && k != "image_ids" {
				continue
			}
			out[k] = canonicalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = canonicalize(e)
		}
		return out
	}
	return v
}
