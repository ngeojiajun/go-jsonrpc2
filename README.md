# go-jsonrpc2

A simple, type-safe JSON-RPC 2.0 application framework for Go with transport-neutral server primitives and optional Gin integration.

## Features

- **Type-Safe Method Handlers**: Use generics to define strongly-typed RPC methods
- **Batch Request Support**: Handle multiple requests in a single HTTP call
- **Notification Support**: Process fire-and-forget requests (no response required)
- **Automatic Validation**: Built-in validation via the `Validator` interface
- **Error Handling**: Proper JSON-RPC 2.0 error responses with standardized error codes
- **Observability**: OpenTelemetry tracing and structured logging with slog
- **Panic Recovery**: Gracefully handles panics in handlers
- **Custom Logging**: Configure custom loggers via options
- **Transport-Neutral Core**: Use the same application with `net/http`, Gin, websocket message handlers, or local in-process callers

## Installation

```bash
go get github.com/ngeojiajun/go-jsonrpc2
```

## Migration From v0.0.5

`v0.0.5` to the current version includes intentional breaking changes.

### 1. Handler signatures now use `context.Context`

Before:

```go
jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
	return p.A + p.B, nil
})
```

After:

```go
jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
	return p.A + p.B, nil
})
```

If you previously depended on `*gin.Context`, move framework-level data access to helpers on `context.Context`. Transport-specific concerns should stay in the adapter layer.

### 2. `JSONRPCApplication` is no longer tied to Gin

Before, the common integration point was Gin:

```go
router.POST("/rpc", app.Handle)
```

That still works, but the application now also implements standard `net/http`:

```go
http.Handle("/rpc", app)
```

Additional server entrypoints are available:

- `ServeHTTP(http.ResponseWriter, *http.Request)`
- `ServeJSON(context.Context, json.RawMessage)`
- `Invoke(context.Context, *Request)`

Use `ServeJSON` for websocket/message transports and `Invoke` for local in-process calls.

### 3. `__context` is now exposed through helper functions

The `__context` extension is still part of the JSON-RPC payload, but handlers no longer need a transport-specific parameter to access it.

Example:

```go
type RequestMeta struct {
	UserID int `json:"user_id"`
}

jsonrpc.AddTypedMethod(app, "whoami", func(ctx context.Context, p struct{}) (int, error) {
	var meta RequestMeta
	if err := jsonrpc.UnmarshalRequestContext(ctx, &meta); err != nil {
		return 0, err
	}
	return meta.UserID, nil
})
```

Available helpers:

- `RequestFromContext`
- `RequestContextRaw`
- `UnmarshalRequestContext`

### 4. Client transport options expanded

`NewClient(...)` still gives you the default HTTP client behavior.

New explicit constructors:

- `NewHTTPClient(...)`
- `NewLocalClient(app)`

The generic helpers `Call`, `Notify`, `AddBatchCall`, and `AddBatchNotification` now accept request options, including `WithRequestContextValue(...)`.

Example:

```go
res, err := jsonrpc.Call[struct{}, int](
	ctx,
	jsonrpc.NewLocalClient(app),
	"whoami",
	struct{}{},
	jsonrpc.WithRequestContextValue(struct {
		UserID int `json:"user_id"`
	}{UserID: 123}),
)
```

### 5. Malformed JSON handling is stricter

The HTTP path now validates request bodies before dispatching.

- malformed JSON returns `CodeParseError`
- syntactically valid but invalid JSON-RPC payloads return `CodeInvalidRequest`

## Quick Start

```go
package main

import (
	"context"
	"net/http"

	jsonrpc "github.com/ngeojiajun/go-jsonrpc2"
)

// Define your parameter types
type SumParams struct {
	A int `json:"a"`
	B int `json:"b"`
}

func main() {
	// Create application
	app := jsonrpc.NewJSONRPCApplication()

	// Register a typed method
	jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	http.Handle("/rpc", app)
	http.ListenAndServe(":8080", nil)
}
```

## Usage Examples

### Simple RPC Call

```bash
curl -X POST http://localhost:8080/rpc \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"sum","params":{"a":2,"b":3}}'
```

Response:
```json
{"jsonrpc":"2.0","id":1,"result":5}
```

### Batch Request

```json
[
  {"jsonrpc":"2.0","id":1,"method":"sum","params":{"a":1,"b":2}},
  {"jsonrpc":"2.0","id":2,"method":"sum","params":{"a":10,"b":20}}
]
```

### Notification (No Response)

```json
{"jsonrpc":"2.0","method":"sum","params":{"a":1,"b":2}}
```

### Custom Validation

Implement the `Validator` interface for automatic validation:

```go
type DivideParams struct {
	A float64 `json:"a"`
	B float64 `json:"b"`
}

func (p DivideParams) Validate() error {
	if p.B == 0 {
		return errors.New("B must not be zero")
	}
	return nil
}

// At somewhere else in your code

jsonrpc.AddTypedMethod(app, "divide", func(ctx context.Context, p DivideParams) (float64, error) {
	return p.A / p.B, nil
})
```

## Transport Adapters

The application now exposes three server entrypoints:

- `ServeHTTP(http.ResponseWriter, *http.Request)` for standard HTTP servers
- `Handle(*gin.Context)` as a thin Gin adapter
- `ServeJSON(context.Context, json.RawMessage)` and `Invoke(context.Context, *Request)` for websocket or local transports

For websocket-style transports, decode the incoming message bytes once and pass them to `ServeJSON`. For local in-process callers, construct a `Request` and call `Invoke`.

## Client Transports

The default `NewClient(...)` constructor returns an HTTP client. You can also construct the transport explicitly with `NewHTTPClient(...)`.

For local in-process calls, use `NewLocalClient(app)`. It routes requests through `ServeJSON`, so it supports single calls, notifications, batch calls, and the optional `__context` extension without going through HTTP.

```go
httpClient := jsonrpc.NewClient("http://localhost:8080/rpc")
localClient := jsonrpc.NewLocalClient(app)
```

Per-request `__context` can be attached from any client transport:

```go
res, err := jsonrpc.Call[struct{}, int](
	ctx,
	localClient,
	"whoami",
	struct{}{},
	jsonrpc.WithRequestContextValue(struct {
		UserID int `json:"user_id"`
	}{UserID: 123}),
)
```

## Accessing `__context`

The optional JSON-RPC extension field `__context` is attached to the handler `context.Context` and can be accessed through helpers without changing the handler signature:

```go
type RequestMeta struct {
	UserID int `json:"user_id"`
}

jsonrpc.AddTypedMethod(app, "whoami", func(ctx context.Context, p struct{}) (int, error) {
	var meta RequestMeta
	if err := jsonrpc.UnmarshalRequestContext(ctx, &meta); err != nil {
		return 0, err
	}
	return meta.UserID, nil
})
```

## Error Handling

The framework automatically returns proper JSON-RPC 2.0 error responses:

- **CodeParseError**: Request body cannot be parsed
- **CodeInvalidRequest**: Invalid JSON-RPC structure
- **CodeMethodNotFound**: Method does not exist
- **CodeInvalidParams**: Parameters fail validation or type checking
- **CodeInternalError**: Handler returned an error or panicked

## Library logging
By default the library will not produce any logs. You can configure a custom logger by passing the `WithJSONRPCLogger` option when creating the application:

```go
import "log/slog"

logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
app := jsonrpc.NewJSONRPCApplication(
	jsonrpc.WithJSONRPCLogger(logger),
)
```

This will become handy when debugging handler panics.

## License

MIT
