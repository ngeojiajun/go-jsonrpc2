package jsonrpc

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// File: types.go
// Provide some typedefs

type Blob []byte

func (b Blob) MarshalJSON() ([]byte, error) {
	if b == nil {
		return []byte("null"), nil
	}
	val := base64.StdEncoding.EncodeToString(b)
	return []byte("\"" + val + "\""), nil
}

func (m *Blob) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errors.New("types.Blob: UnmarshalJSON on nil pointer")
	}
	// data includes surrounding quotes
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}
	val, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	*m = val
	return nil
}

// Represent something that can be validated
type Validator interface {
	Validate() error
}
