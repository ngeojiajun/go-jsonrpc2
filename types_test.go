package jsonrpc

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlobDecoding_Ok(t *testing.T) {
	var b Blob
	err := b.UnmarshalJSON([]byte(`"SGVsbG8sIFdvcmxkIQ=="`))
	assert.NoError(t, err)
	assert.Equal(t, "Hello, World!", string(b))
}

func TestBlobDecoding_InvalidBase64(t *testing.T) {
	var b Blob
	err := b.UnmarshalJSON([]byte(`"InvalidBase64"`))
	assert.Error(t, err)
}

func TestBlobDecoding_NotAString(t *testing.T) {
	var b Blob
	err := b.UnmarshalJSON([]byte(`12345`))
	assert.Error(t, err)
}

func TestBlobDecoding_Empty(t *testing.T) {
	var b Blob
	err := b.UnmarshalJSON([]byte(`""`))
	assert.NoError(t, err)
	assert.Equal(t, "", string(b))
}

func TestBlobDecoding_Null(t *testing.T) {
	var b Blob
	err := b.UnmarshalJSON([]byte(`null`))
	assert.NoError(t, err)
	assert.Empty(t, b)
}

func TestBlobEncoding(t *testing.T) {
	b := Blob("Hello, World!")
	data, err := b.MarshalJSON()
	assert.NoError(t, err)
	assert.Equal(t, `"SGVsbG8sIFdvcmxkIQ=="`, string(data))
}

func TestBlobEncoding_Nil(t *testing.T) {
	var b Blob
	data, err := b.MarshalJSON()
	assert.NoError(t, err)
	assert.Equal(t, `null`, string(data))
}
