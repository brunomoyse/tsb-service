package hubrisetest

import (
	"encoding/json"
	"fmt"
	"sort"
)

// DroppedFields returns the paths of the fields present in raw (with a value that is not empty:
// not null, false, 0, "", [] or {}) that are missing once decoded is marshalled back. It detects
// the fields a Go type does not model, whatever custom unmarshalers sit in between, which
// json.Decoder.DisallowUnknownFields cannot (it does not reach into a custom UnmarshalJSON).
//
// The mock uses it to refuse request bodies with unknown fields, and the contract tests to prove
// that the types model every field of the recorded HubRise exchanges.
func DroppedFields(raw []byte, decoded any) ([]string, error) {
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		return nil, fmt.Errorf("hubrisetest: dropped fields: %w", err)
	}
	back, err := json.Marshal(decoded)
	if err != nil {
		return nil, fmt.Errorf("hubrisetest: dropped fields: %w", err)
	}
	var got any
	if err := json.Unmarshal(back, &got); err != nil {
		return nil, fmt.Errorf("hubrisetest: dropped fields: %w", err)
	}
	var dropped []string
	walkDropped("", want, got, &dropped)
	sort.Strings(dropped)
	return dropped, nil
}

func walkDropped(path string, want, got any, out *[]string) {
	switch w := want.(type) {
	case map[string]any:
		g, _ := got.(map[string]any)
		for k, wv := range w {
			if isEmptyJSON(wv) {
				continue
			}
			gv, ok := g[k]
			if !ok {
				*out = append(*out, path+"."+k)
				continue
			}
			walkDropped(path+"."+k, wv, gv, out)
		}
	case []any:
		g, _ := got.([]any)
		for i, wv := range w {
			if i >= len(g) {
				if !isEmptyJSON(wv) {
					*out = append(*out, fmt.Sprintf("%s[%d]", path, i))
				}
				continue
			}
			walkDropped(fmt.Sprintf("%s[%d]", path, i), wv, g[i], out)
		}
	}
}

func isEmptyJSON(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case float64:
		return x == 0
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}
