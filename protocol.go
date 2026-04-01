package jsonrpc

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ID is a JSON-RPC 2.0 ID, which can be a string, a number, or null.
type ID struct {
	Num      uint64
	Str      string
	IsString bool
}

// String returns the string representation of the ID.
func (id ID) String() string {
	if id.IsString {
		return id.Str
	}
	return strconv.FormatUint(id.Num, 10)
}

// MarshalJSON implements json.Marshaler.
func (id ID) MarshalJSON() ([]byte, error) {
	if id.IsString {
		return json.Marshal(id.Str)
	}
	return json.Marshal(id.Num)
}

// UnmarshalJSON implements json.Unmarshaler.
func (id *ID) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		// In JSON-RPC 2.0, ID can be null if it's a response to a request with a null ID or if ID couldn't be parsed.
		// However, usually we handle it by keeping it as IsString=false, Num=0 or using a pointer.
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		id.IsString = true
		return json.Unmarshal(data, &id.Str)
	}
	id.IsString = false
	return json.Unmarshal(data, &id.Num)
}

// Error represents a JSON-RPC 2.0 error.
type Error struct {
	Code    int64            `json:"code"`
	Message string           `json:"message"`
	Data    *json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("jsonrpc2: code %d, message: %s", e.Code, e.Message)
}

// SetError sets the data field of the error by marshalling the given value.
func (e *Error) SetError(data any) error {
	if data == nil {
		e.Data = nil
		return nil
	}
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	raw := json.RawMessage(b)
	e.Data = &raw
	return nil
}

// Request represents a JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string           `json:"jsonrpc"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
	ID      *ID              `json:"id,omitempty"`
	Notif   bool             `json:"-"`
	Context *json.RawMessage `json:"__context,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler and detects if the request is a notification.
func (r *Request) UnmarshalJSON(data []byte) error {
	type Alias Request
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Notif = (r.ID == nil)
	return nil
}

// MarshalJSON ensures JSONRPC is set to "2.0".
func (r Request) MarshalJSON() ([]byte, error) {
	type Alias Request
	if r.JSONRPC == "" {
		r.JSONRPC = "2.0"
	}
	return json.Marshal((Alias)(r))
}

// Response represents a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string           `json:"jsonrpc"`
	Result  *json.RawMessage `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
	ID      *ID              `json:"id"`
}

// MarshalJSON ensures JSONRPC is set to "2.0".
func (r Response) MarshalJSON() ([]byte, error) {
	type Alias Response
	if r.JSONRPC == "" {
		r.JSONRPC = "2.0"
	}
	return json.Marshal((Alias)(r))
}

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     int64 = -32700
	CodeInvalidRequest int64 = -32600
	CodeMethodNotFound int64 = -32601
	CodeInvalidParams  int64 = -32602
	CodeInternalError  int64 = -32603
)
