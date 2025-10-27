package domain

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
)

// NullableJSON is a JSON field that can be NULL in the database
type NullableJSON []byte

// Scan implements sql.Scanner interface
func (nj *NullableJSON) Scan(value interface{}) error {
	if value == nil {
		*nj = nil
		return nil
	}

	switch v := value.(type) {
	case []byte:
		*nj = make([]byte, len(v))
		copy(*nj, v)
		return nil
	case string:
		*nj = []byte(v)
		return nil
	default:
		return errors.New("failed to scan NullableJSON: invalid type")
	}
}

// Value implements driver.Valuer interface
func (nj NullableJSON) Value() (driver.Value, error) {
	if nj == nil {
		return nil, nil
	}
	return []byte(nj), nil
}

// MarshalJSON implements json.Marshaler interface
func (nj NullableJSON) MarshalJSON() ([]byte, error) {
	if nj == nil {
		return []byte("null"), nil
	}
	return nj, nil
}

// UnmarshalJSON implements json.Unmarshaler interface
func (nj *NullableJSON) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*nj = nil
		return nil
	}
	*nj = make([]byte, len(data))
	copy(*nj, data)
	return nil
}

// Unmarshal unmarshals the JSON into the provided interface
func (nj NullableJSON) Unmarshal(v interface{}) error {
	if nj == nil {
		return nil
	}
	return json.Unmarshal(nj, v)
}

// IsNull returns true if the JSON is null
func (nj NullableJSON) IsNull() bool {
	return nj == nil
}
