package jsonrpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jsonrpc "github.com/ngeojiajun/go-jsonrpc2"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

func createServer(app *jsonrpc.JSONRPCApplication) *httptest.Server {
	r := gin.New()
	r.POST("/rpc", app.Handle)
	return httptest.NewServer(r)
}

func TestClient_Call(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL + "/rpc")
	ctx := context.Background()

	res, err := jsonrpc.Call[SumParams, int](ctx, client, "sum", SumParams{A: 10, B: 20})
	assert.NoError(t, err)
	assert.Equal(t, 30, res)
}

func TestClient_Notify(t *testing.T) {
	var notified atomic.Int32
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "notify", func(ctx *gin.Context, p SumParams) (int, error) {
		notified.Add(1)
		return 0, nil
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL + "/rpc")
	ctx := context.Background()

	err := jsonrpc.Notify(ctx, client, "notify", SumParams{A: 10, B: 20})
	assert.NoError(t, err)
	assert.Eventually(t, func() bool { return notified.Load() == 1 }, 1*time.Second, 10*time.Millisecond)
}

func TestClient_Batch(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})
	_ = jsonrpc.AddTypedMethod(app, "concat", func(ctx *gin.Context, p []string) (string, error) {
		res := ""
		for _, s := range p {
			res += s
		}
		return res, nil
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL + "/rpc")
	ctx := context.Background()

	batch := client.NewBatch()
	sumCall, err := jsonrpc.AddBatchCall[SumParams, int](batch, "sum", SumParams{A: 1, B: 2})
	require.NoError(t, err)

	concatCall, err := jsonrpc.AddBatchCall[[]string, string](batch, "concat", []string{"hello", " ", "world"})
	require.NoError(t, err)

	err = batch.Execute(ctx)
	assert.NoError(t, err)

	sum, err := sumCall.Wait()
	assert.NoError(t, err)
	assert.Equal(t, 3, sum)

	concat, err := concatCall.Wait()
	assert.NoError(t, err)
	assert.Equal(t, "hello world", concat)
}

func TestClient_Error(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "fail", func(ctx *gin.Context, p struct{}) (int, error) {
		return 0, fmt.Errorf("intentional failure")
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL + "/rpc")
	ctx := context.Background()

	_, err := jsonrpc.Call[struct{}, int](ctx, client, "fail", struct{}{})
	assert.Error(t, err)

	var rpcErr *jsonrpc2.Error
	assert.True(t, errors.As(err, &rpcErr))
	assert.Equal(t, int64(jsonrpc2.CodeInternalError), rpcErr.Code)
}

func TestClient_Options(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "check_header", func(ctx *gin.Context, p struct{}) (string, error) {
		return ctx.GetHeader("X-Custom-Header"), nil
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL+"/rpc", jsonrpc.WithHeader("X-Custom-Header", "hello"))
	ctx := context.Background()

	res, err := jsonrpc.Call[struct{}, string](ctx, client, "check_header", struct{}{})
	assert.NoError(t, err)
	assert.Equal(t, "hello", res)
}

type mockIDGenerator struct {
	id string
}

func (m *mockIDGenerator) NextID() jsonrpc2.ID {
	return jsonrpc2.ID{Str: m.id, IsString: true}
}

func TestClient_CustomIDGenerator(t *testing.T) {
	var capturedID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonrpc2.Request
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		capturedID = req.ID.String()

		result := json.RawMessage("null")
		resp, _ := json.Marshal(jsonrpc2.Response{ID: req.ID, Result: &result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	}))
	defer server.Close()

	client := jsonrpc.NewClient(server.URL, jsonrpc.WithIDGenerator(&mockIDGenerator{id: "custom-id"}))
	ctx := context.Background()

	_, err := jsonrpc.Call[struct{}, any](ctx, client, "any", struct{}{})
	assert.NoError(t, err)
	assert.Equal(t, "\"custom-id\"", capturedID)
}

func TestClient_OTelPropagation(t *testing.T) {
	// Setup a propagator
	prevPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer otel.SetTextMapPropagator(prevPropagator)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check for traceparent header
		if r.Header.Get("traceparent") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := json.RawMessage("null")
		resp, _ := json.Marshal(jsonrpc2.Response{ID: jsonrpc2.ID{Num: 1, IsString: false}, Result: &result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	}))
	defer server.Close()

	client := jsonrpc.NewClient(server.URL)
	ctx := context.Background()

	// Create a real tracer provider to generate valid trace context
	tp := trace.NewTracerProvider()
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(ctx, "test-span")
	defer span.End()

	_, err := jsonrpc.Call[struct{}, any](ctx, client, "any", struct{}{})
	assert.NoError(t, err)
}
