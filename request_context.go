package jsonrpc

import (
	"context"
	"encoding/json"
	"errors"
)

type requestContextKey struct{}

var errNoRequestContext = errors.New("jsonrpc: request context is not available")

func withRequest(ctx context.Context, req *Request) context.Context {
	return context.WithValue(ctx, requestContextKey{}, req)
}

// RequestFromContext returns the current JSON-RPC request metadata attached by the framework.
func RequestFromContext(ctx context.Context) (*Request, bool) {
	req, ok := ctx.Value(requestContextKey{}).(*Request)
	return req, ok
}

// RequestContextRaw returns the raw __context payload from the current request.
func RequestContextRaw(ctx context.Context) (json.RawMessage, bool) {
	req, ok := RequestFromContext(ctx)
	if !ok || req == nil || req.Context == nil {
		return nil, false
	}
	return *req.Context, true
}

// UnmarshalRequestContext decodes the optional __context payload into dst.
func UnmarshalRequestContext[T any](ctx context.Context, dst *T) error {
	raw, ok := RequestContextRaw(ctx)
	if !ok {
		return errNoRequestContext
	}
	return json.Unmarshal(raw, dst)
}
