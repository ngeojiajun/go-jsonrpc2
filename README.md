# go-jsonrpc2

A simple, type-safe JSON-RPC 2.0 application framework for Go, built on top of Gin and Sourcegraph's jsonrpc2.

## Features

- **Type-Safe Method Handlers**: Use generics to define strongly-typed RPC methods
- **Batch Request Support**: Handle multiple requests in a single HTTP call
- **Notification Support**: Process fire-and-forget requests (no response required)
- **Automatic Validation**: Built-in validation via the `Validator` interface
- **Error Handling**: Proper JSON-RPC 2.0 error responses with standardized error codes
- **Observability**: OpenTelemetry tracing and structured logging with slog
- **Panic Recovery**: Gracefully handles panics in handlers
- **Custom Logging**: Configure custom loggers via options

## Installation

```bash
go get github.com/ngeojiajun/go-jsonrpc2
```

## Quick Start

```go
package main

import (
	"github.com/gin-gonic/gin"
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
	jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	// Set up Gin router
	router := gin.Default()
	router.POST("/rpc", app.Handle)

	router.Run(":8080")
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

jsonrpc.AddTypedMethod(app, "divide", func(ctx *gin.Context, p DivideParams) (float64, error) {
	return p.A / p.B, nil
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
