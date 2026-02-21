package jsonrpc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	jsonrpc "github.com/ngeojiajun/go-jsonrpc2"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/stretchr/testify/assert"
)

type SumParams struct {
	A int `json:"a"`
	B int `json:"b"`
}

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

type Unmarshallable struct{}

func (p Unmarshallable) MarshalJSON() ([]byte, error) {
	return nil, errors.New("cannot marshal")
}

func setupRouter(app *jsonrpc.JSONRPCApplication) *gin.Engine {
	r := gin.Default()
	r.POST("/rpc", app.Handle)
	return r
}

func decodeResult(t *testing.T, p *json.RawMessage) int {
	var r int
	if err := json.Unmarshal([]byte(*p), &r); err != nil {
		t.Fatalf("cannot parse request: %v", err)
	}
	return r
}
func decodeResultFloat64(t *testing.T, p *json.RawMessage) float64 {
	var r float64
	if err := json.Unmarshal([]byte(*p), &r); err != nil {
		t.Fatalf("cannot parse request: %v", err)
	}
	return r
}

func TestAddTypedMethod_Success(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	err := jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})
	assert.NoError(t, err)

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"sum","params":{"a":2,"b":3}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)

	var resp jsonrpc2.Response
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.Nil(t, resp.Error)
	assert.EqualValues(t, 5, decodeResult(t, resp.Result))
}

func TestAddTypedMethod_InvalidParams(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	router := setupRouter(app)

	// "a" is string → should fail
	reqBody := `{"jsonrpc":"2.0","id":1,"method":"sum","params":{"a":"bad","b":3}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var resp jsonrpc2.Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInvalidParams, resp.Error.Code)
}

func TestAddTypedMethod_CustomValidatorOk(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "div", func(ctx *gin.Context, p DivideParams) (float64, error) {
		return p.A / p.B, nil
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"div","params":{"a":5,"b":4}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var resp jsonrpc2.Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Nil(t, resp.Error)
	assert.EqualValues(t, float64(1.25), decodeResultFloat64(t, resp.Result))
}

func TestAddTypedMethod_CustomValidatorFail(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "div", func(ctx *gin.Context, p DivideParams) (float64, error) {
		return p.A / p.B, nil
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"div","params":{"a":5,"b":0}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var resp jsonrpc2.Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInvalidParams, resp.Error.Code)
	assert.EqualValues(t, "B must not be zero", resp.Error.Message)
}

func TestUnknownMethod(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","id":1,"method":"doesNotExist"}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var resp jsonrpc2.Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeMethodNotFound, resp.Error.Code)
}

func TestBatchRequests(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	router := setupRouter(app)

	reqBody := `[{"jsonrpc":"2.0","id":1,"method":"sum","params":{"a":1,"b":2}},
	             {"jsonrpc":"2.0","id":2,"method":"sum","params":{"a":10,"b":20}}]`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var resp []jsonrpc2.Response
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Len(t, resp, 2)
	assert.EqualValues(t, float64(3), decodeResult(t, resp[0].Result))
	assert.EqualValues(t, float64(30), decodeResult(t, resp[1].Result))
}

func TestNotification_NoResponse(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	router := setupRouter(app)

	// Notification → no "id"
	reqBody := `{"jsonrpc":"2.0","method":"sum","params":{"a":1,"b":2}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestRPCPanic(t *testing.T) {
	// The framework must return proper response even the handler paniced
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		panic("boom")
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","method":"sum","params":{"a":1,"b":2}, "id":123}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp jsonrpc2.Response
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInternalError, resp.Error.Code)
}

func TestRPCPanicInNotification(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		panic("boom")
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","method":"sum","params":{"a":1,"b":2}}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestRPCUndecoableParams(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "sum", func(ctx *gin.Context, p SumParams) (int, error) {
		return p.A + p.B, nil
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","method":"sum","params":"not an object", "id":123}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp jsonrpc2.Response
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInvalidParams, resp.Error.Code)
}

func TestRPCUnmarshallableResponse(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "bad", func(ctx *gin.Context, p struct{}) (Unmarshallable, error) {
		return Unmarshallable{}, nil
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","method":"bad","params":{}, "id":123}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp jsonrpc2.Response
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInternalError, resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "Error occured while marshalling response")
}

func TestRPCError(t *testing.T) {
	app := jsonrpc.NewJSONRPCApplication()
	_ = jsonrpc.AddTypedMethod(app, "bad", func(ctx *gin.Context, p struct{}) (int, error) {
		return 0, errors.New("something went wrong")
	})

	router := setupRouter(app)

	reqBody := `{"jsonrpc":"2.0","method":"bad","params":{}, "id":123}`
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/rpc", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp jsonrpc2.Response
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	assert.NoError(t, err)
	assert.NotNil(t, resp.Error)
	assert.EqualValues(t, jsonrpc2.CodeInternalError, resp.Error.Code)
	// The error message should be generic, not exposing internal error details
	assert.EqualValues(t, "Error occured while processing request", resp.Error.Message)
}
