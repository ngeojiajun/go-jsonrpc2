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

	jsonrpc "github.com/ngeojiajun/go-jsonrpc2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
)

func createServer(app *jsonrpc.JSONRPCApplication) *httptest.Server {
	return httptest.NewServer(app)
}

func TestClient_Call(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
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

func TestLocalClient_Call(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	client := jsonrpc.NewLocalClient(app)
	ctx := context.Background()

	res, err := jsonrpc.Call[SumParams, int](ctx, client, "sum", SumParams{A: 3, B: 9})
	assert.NoError(t, err)
	assert.Equal(t, 12, res)
}

func TestClient_Notify(t *testing.T) {
	var notified atomic.Int32
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "notify", func(ctx context.Context, p SumParams) (int, error) {
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

func TestLocalClient_Notify(t *testing.T) {
	var notified atomic.Int32
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "notify", func(ctx context.Context, p SumParams) (int, error) {
		notified.Add(1)
		return 0, nil
	})

	client := jsonrpc.NewLocalClient(app)
	ctx := context.Background()

	err := jsonrpc.Notify(ctx, client, "notify", SumParams{A: 10, B: 20})
	assert.NoError(t, err)
	assert.Equal(t, int32(1), notified.Load())
}

func TestClient_Batch(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})
	_ = jsonrpc.AddTypedMethod(app, "concat", func(ctx context.Context, p []string) (string, error) {
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

func TestLocalClient_Batch(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx context.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	client := jsonrpc.NewLocalClient(app)
	ctx := context.Background()

	batch := client.NewBatch()
	sumCall, err := jsonrpc.AddBatchCall[SumParams, int](batch, "sum", SumParams{A: 5, B: 6})
	require.NoError(t, err)

	err = batch.Execute(ctx)
	assert.NoError(t, err)

	sum, err := sumCall.Wait()
	assert.NoError(t, err)
	assert.Equal(t, 11, sum)
}

func TestClient_Error(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "fail", func(ctx context.Context, p struct{}) (int, error) {
		return 0, fmt.Errorf("intentional failure")
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL + "/rpc")
	ctx := context.Background()

	_, err := jsonrpc.Call[struct{}, int](ctx, client, "fail", struct{}{})
	assert.Error(t, err)

	var rpcErr *jsonrpc.Error
	assert.True(t, errors.As(err, &rpcErr))
	assert.Equal(t, jsonrpc.CodeInternalError, rpcErr.Code)
}

func TestClient_Options(t *testing.T) {
	var capturedHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-Custom-Header")
		result := json.RawMessage(`"ok"`)
		body, err := json.Marshal(jsonrpc.Response{
			ID:     &jsonrpc.ID{Num: 1},
			Result: &result,
		})
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := jsonrpc.NewClient(server.URL, jsonrpc.WithHeader("X-Custom-Header", "hello"))
	ctx := context.Background()

	res, err := jsonrpc.Call[struct{}, string](ctx, client, "check_header", struct{}{})
	assert.NoError(t, err)
	assert.Equal(t, "ok", res)
	assert.Equal(t, "hello", capturedHeader)
}

func TestClient_RequestContext(t *testing.T) {
	type requestMeta struct {
		UserID int `json:"user_id"`
	}

	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "whoami", func(ctx context.Context, p struct{}) (int, error) {
		var meta requestMeta
		if err := jsonrpc.UnmarshalRequestContext(ctx, &meta); err != nil {
			return 0, err
		}
		return meta.UserID, nil
	})

	server := createServer(app)
	defer server.Close()

	client := jsonrpc.NewClient(server.URL)
	ctx := context.Background()

	res, err := jsonrpc.Call[struct{}, int](ctx, client, "whoami", struct{}{}, jsonrpc.WithRequestContextValue(requestMeta{UserID: 42}))
	assert.NoError(t, err)
	assert.Equal(t, 42, res)
}

func TestLocalClient_RequestContext(t *testing.T) {
	type requestMeta struct {
		UserID int `json:"user_id"`
	}

	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "whoami", func(ctx context.Context, p struct{}) (int, error) {
		var meta requestMeta
		if err := jsonrpc.UnmarshalRequestContext(ctx, &meta); err != nil {
			return 0, err
		}
		return meta.UserID, nil
	})

	client := jsonrpc.NewLocalClient(app)
	ctx := context.Background()

	res, err := jsonrpc.Call[struct{}, int](ctx, client, "whoami", struct{}{}, jsonrpc.WithRequestContextValue(requestMeta{UserID: 7}))
	assert.NoError(t, err)
	assert.Equal(t, 7, res)
}

type mockIDGenerator struct {
	id string
}

func (m *mockIDGenerator) NextID() *jsonrpc.ID {
	return &jsonrpc.ID{Str: m.id, IsString: true}
}

func TestClient_CustomIDGenerator(t *testing.T) {
	var capturedID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonrpc.Request
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		capturedID = req.ID.String()

		result := json.RawMessage("null")
		resp, _ := json.Marshal(jsonrpc.Response{ID: req.ID, Result: &result})
		w.Header().Set("Content-Type", "application/json")
		w.Write(resp)
	}))
	defer server.Close()

	client := jsonrpc.NewClient(server.URL, jsonrpc.WithIDGenerator(&mockIDGenerator{id: "custom-id"}))
	ctx := context.Background()

	_, err := jsonrpc.Call[struct{}, any](ctx, client, "any", struct{}{})
	assert.NoError(t, err)
	assert.Equal(t, "custom-id", capturedID)
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
		resp, _ := json.Marshal(jsonrpc.Response{ID: &jsonrpc.ID{Num: 1, IsString: false}, Result: &result})
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
