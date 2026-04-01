package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// File: application.go
// Provide simple handling for jsonrpc

// Base type for RPC Method
type RPCMethod = func(context.Context, json.RawMessage) (any, error)

type JSONRPCApplication struct {
	methods map[string]RPCMethod
	tracer  trace.Tracer
	logger  *slog.Logger
}

var strReaderPool = sync.Pool{
	New: func() any {
		return new(strings.Reader)
	},
}

type JSONRPCApplicationOptions = func(f *JSONRPCApplication)

// Create a JSON RPC application
func NewJSONRPCApplication(opts ...JSONRPCApplicationOptions) *JSONRPCApplication {
	app := &JSONRPCApplication{
		methods: make(map[string]RPCMethod),
		tracer:  otel.Tracer("github.com/ngeojiajun/go-jsonrpc2/application"),
		logger:  slog.New(slog.DiscardHandler), // default to discard, can be overridden by options
	}
	for _, opt := range opts {
		opt(app)
	}
	return app
}

func WithJSONRPCLogger(logger *slog.Logger) JSONRPCApplicationOptions {
	return func(app *JSONRPCApplication) {
		app.logger = logger
	}
}

func writeJSONResponse(w http.ResponseWriter, status int, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(body)
	return err
}

func writeJSONRPCError(w http.ResponseWriter, req *Request, code int64, msg string, data any) error {
	var res Response
	res.Error = BuildJSONRPCError(code, msg, data)
	if req != nil {
		res.ID = req.ID
	}
	return writeJSONResponse(w, http.StatusOK, res)
}

// Build a JSONRPC Error that you can return as Error
func BuildJSONRPCError(code int64, msg string, data any) *Error {
	var err = &Error{
		Code:    code,
		Message: msg,
	}
	err.SetError(data)
	return err
}

// Add a method into the application so it can serves the request
func (app *JSONRPCApplication) AddMethod(name string, method RPCMethod) error {
	if _, exists := app.methods[name]; exists {
		return fmt.Errorf("%s is already declared", name)
	}
	app.methods[name] = method
	return nil
}

// Add a method into the application so it can serves the request. This variant will enforce type checking
func AddTypedMethod[T any, R any](app *JSONRPCApplication, name string, fn func(context.Context, T) (R, error)) error {
	f := func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p T
		strReader := strReaderPool.Get().(*strings.Reader)
		defer strReaderPool.Put(strReader)
		strReader.Reset(string(raw))
		reader := json.NewDecoder(strReader)
		reader.DisallowUnknownFields()
		if err := reader.Decode(&p); err != nil {
			return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
		}
		// Extension: If the object implement custom validation
		if p, ok := any(p).(Validator); ok {
			if err := p.Validate(); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
			}
		}
		return fn(ctx, p)
	}
	return app.AddMethod(name, f)
}

func parseRawPayload(data []byte) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return nil, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (app *JSONRPCApplication) dispatchJSON(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	reqCtx, span := app.tracer.Start(ctx, "JSONRPCApplication.dispatchJSON")
	defer span.End()

	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return encodePayload(Response{
			Error: BuildJSONRPCError(CodeInvalidRequest, "Invalid request", "empty payload"),
		})
	}

	// Check if batch or single request
	if trimmed[0] == '[' {
		var reqs []Request
		if err := json.Unmarshal(raw, &reqs); err != nil {
			return encodePayload(Response{
				Error: BuildJSONRPCError(CodeInvalidRequest, "Invalid batch request", err.Error()),
			})
		}
		var responses []Response
		for _, r := range reqs {
			if res := app.handleSingle(reqCtx, &r); res != nil {
				responses = append(responses, *res)
			}
		}
		if len(responses) > 0 {
			return encodePayload(responses)
		}
		return nil, nil
	} else {
		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			return encodePayload(Response{
				Error: BuildJSONRPCError(CodeInvalidRequest, "Invalid request", err.Error()),
			})
		}
		if res := app.handleSingle(reqCtx, &req); res != nil {
			return encodePayload(res)
		}
		return nil, nil
	}
}

// Handle the request, this is meant to be added directly as gin application
func (app *JSONRPCApplication) Handle(ctx *gin.Context) {
	app.ServeHTTP(ctx.Writer, ctx.Request)
}

// ServeHTTP exposes the application as a standard net/http handler.
func (app *JSONRPCApplication) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqCtx, span := app.tracer.Start(r.Context(), "JSONRPCApplication.ServeHTTP")
	defer span.End()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		_ = writeJSONRPCError(w, nil, CodeParseError, "Cannot parse the request", err.Error())
		return
	}

	raw, err := parseRawPayload(body)
	if err != nil {
		_ = writeJSONRPCError(w, nil, CodeParseError, "Cannot parse the request", err.Error())
		return
	}

	payload, err := app.dispatchJSON(reqCtx, raw)
	if err != nil {
		_ = writeJSONRPCError(w, nil, CodeInternalError, "Error occured while marshalling response", err.Error())
		return
	}
	if payload == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

// ServeJSON handles a raw JSON-RPC payload and returns the encoded response payload.
// A nil response means the request resolved to notifications only.
func (app *JSONRPCApplication) ServeJSON(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	validated, err := parseRawPayload(raw)
	if err != nil {
		return nil, BuildJSONRPCError(CodeParseError, "Cannot parse the request", err.Error())
	}
	return app.dispatchJSON(ctx, validated)
}

// Invoke handles a single request directly, which is useful for local transports.
func (app *JSONRPCApplication) Invoke(ctx context.Context, req *Request) *Response {
	return app.handleSingle(ctx, req)
}

func (app *JSONRPCApplication) handleSingle(ctx context.Context, req *Request) (res *Response) {
	// Collecting info
	rpcStart := time.Now()
	var reqIDStr string
	if req.ID != nil {
		reqIDStr = req.ID.String()
	}
	reqCtx := withRequest(ctx, req)
	reqCtx, span := app.tracer.Start(reqCtx, "JSONRPCApplication.handleSingle",
		trace.WithAttributes(
			attribute.String("method", req.Method),
			attribute.Bool("is_notification", req.Notif),
			attribute.String("id", reqIDStr),
		),
	)
	// Setup crash handler here, and also measure the timing
	defer func() {
		callInfo := slog.Group("call",
			slog.String("id", reqIDStr),
			slog.String("method", req.Method),
			slog.Bool("IsNotification", req.Notif),
		)
		if err := recover(); err != nil {
			app.logger.ErrorContext(reqCtx,
				"A fatal error occured while processing RPC call/notification",
				callInfo,
				slog.Any("error", err),
			)
			if req.Notif {
				res = nil
			} else {
				res = &Response{
					ID:    req.ID,
					Error: BuildJSONRPCError(CodeInternalError, "Internal error occured", nil),
				}
			}
			span.SetStatus(codes.Error, "Handler paniced")
		} else {
			if req.Notif {
				// For notification, we will returning nothing. Because of this we will log it extensively
				level := slog.LevelInfo
				var rpcError error
				if res != nil && res.Error != nil {
					level = slog.LevelError
					rpcError = res.Error
					span.RecordError(rpcError)
					span.SetStatus(codes.Error, "Processing failed")
				} else {
					span.SetStatus(codes.Ok, "Processing succeeded")
				}
				slog.Log(reqCtx, level,
					"Processed a RPC notification",
					callInfo,
					slog.Bool("failed", level == slog.LevelError),
					slog.Any("error", rpcError),
					slog.Any("timeTaken", time.Since(rpcStart)),
				)
				res = nil
			} else {
				failed := false
				if res != nil && res.Error != nil {
					span.RecordError(res.Error)
					span.SetStatus(codes.Error, "Processing failed")
					failed = true
				} else {
					span.SetStatus(codes.Ok, "Processing succeeded")
				}
				app.logger.InfoContext(
					reqCtx,
					"Processed a RPC call",
					callInfo,
					slog.Bool("failed", failed),
					slog.Any("timeTaken", time.Since(rpcStart)),
				)
			}
		}
		span.End()
	}()

	method, ok := app.methods[req.Method]
	if !ok {
		res = &Response{
			ID: req.ID,
			Error: &Error{
				Code:    CodeMethodNotFound,
				Message: fmt.Sprintf("Method %s not found", req.Method),
			},
		}
		return
	}

	var raw json.RawMessage = json.RawMessage("null")
	if req.Params != nil {
		raw = *req.Params
	}

	result, err := method(reqCtx, raw)
	if err != nil {
		app.logger.ErrorContext(reqCtx, "Error occured while processing RPC", slog.String("method", req.Method), slog.Any("error", err), slog.Any("timeTaken", time.Since(rpcStart)))
		switch e := err.(type) {
		case *Error:
			res = &Response{ID: req.ID, Error: e}
			return
		default:
			res = &Response{
				ID: req.ID,
				Error: BuildJSONRPCError(
					CodeInternalError,
					"Error occured while processing request",
					err.Error(),
				),
			}
			return
		}
	}

	// We do not need to brother trying to perform marshalling for notification
	if req.Notif {
		return
	}

	responseRaw, err := json.Marshal(result)
	if err != nil {
		app.logger.ErrorContext(reqCtx, "RPC succeeded but JSON marshalling failed", slog.String("method", req.Method), slog.Any("error", err))
		res = &Response{
			ID: req.ID,
			Error: BuildJSONRPCError(
				CodeInternalError,
				"Error occured while marshalling response",
				err.Error(),
			),
		}
		return
	}

	responseJSON := json.RawMessage(responseRaw)

	res = &Response{
		ID:     req.ID,
		Result: &responseJSON,
	}
	return
}
