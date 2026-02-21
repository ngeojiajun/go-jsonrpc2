package jsonrpc

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// File: application.go
// Provide simple handling for jsonrpc

// Base type for RPC Method
type RPCMethod = func(context *gin.Context, request json.RawMessage) (any, error)

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

func raiseJSONRPCError(ctx *gin.Context, req *jsonrpc2.Request, code int64, msg string, data any) {
	var res jsonrpc2.Response
	res.Error = BuildJSONRPCError(code, msg, data)
	if req != nil {
		res.ID = req.ID
	}
	ctx.AbortWithStatusJSON(http.StatusOK, res)
}

// Build a jsonrpc2.Error that you can return as Error
func BuildJSONRPCError(code int64, msg string, data any) *jsonrpc2.Error {
	var error = &jsonrpc2.Error{
		Code:    code,
		Message: msg,
	}
	error.SetError(data)
	return error
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
func AddTypedMethod[T any, R any](app *JSONRPCApplication, name string, fn func(*gin.Context, T) (R, error)) error {
	f := func(ctx *gin.Context, raw json.RawMessage) (any, error) {
		var p T
		strReader := strReaderPool.Get().(*strings.Reader)
		defer strReaderPool.Put(strReader)
		strReader.Reset(string(raw))
		reader := json.NewDecoder(strReader)
		reader.DisallowUnknownFields()
		if err := reader.Decode(&p); err != nil {
			return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: err.Error()}
		}
		// Extension: If the object implement custom validation
		if p, ok := any(p).(Validator); ok {
			if err := p.Validate(); err != nil {
				return nil, &jsonrpc2.Error{Code: jsonrpc2.CodeInvalidParams, Message: err.Error()}
			}
		}
		return fn(ctx, p)
	}
	return app.AddMethod(name, f)
}

// Handle the request, this is meant to be added directly as gin application
func (app *JSONRPCApplication) Handle(ctx *gin.Context) {
	reqCtx, span := app.tracer.Start(ctx.Request.Context(), "JSONRPCApplication.Handle")
	defer span.End()
	ctx.Request = ctx.Request.WithContext(reqCtx)

	var raw json.RawMessage
	if err := ctx.ShouldBindBodyWithJSON(&raw); err != nil {
		raiseJSONRPCError(ctx, nil, jsonrpc2.CodeParseError, "Cannot parse the request", err.Error())
		return
	}

	// Check if batch or single request
	if len(raw) > 0 && raw[0] == '[' {
		var reqs []jsonrpc2.Request
		if err := json.Unmarshal(raw, &reqs); err != nil {
			raiseJSONRPCError(ctx, nil, jsonrpc2.CodeInvalidRequest, "Invalid batch request", err.Error())
			return
		}
		var responses []jsonrpc2.Response
		for _, r := range reqs {
			if res := app.handleSingle(ctx, &r); res != nil {
				responses = append(responses, *res)
			}
		}
		if len(responses) > 0 {
			ctx.JSON(http.StatusOK, responses)
		} else {
			// all were notifications → return nothing
			ctx.Status(http.StatusNoContent)
		}
	} else {
		var req jsonrpc2.Request
		if err := json.Unmarshal(raw, &req); err != nil {
			raiseJSONRPCError(ctx, nil, jsonrpc2.CodeInvalidRequest, "Invalid request", err.Error())
			return
		}
		if res := app.handleSingle(ctx, &req); res != nil {
			ctx.JSON(http.StatusOK, res)
		} else {
			// notification → no response
			ctx.Status(http.StatusNoContent)
		}
	}
}

func (app *JSONRPCApplication) handleSingle(ctx *gin.Context, req *jsonrpc2.Request) (res *jsonrpc2.Response) {
	// Collecting info
	rpcStart := time.Now()
	reqCtx, span := app.tracer.Start(ctx.Request.Context(), "JSONRPCApplication.handleSingle",
		trace.WithAttributes(
			attribute.String("method", req.Method),
			attribute.Bool("is_notification", req.Notif),
			attribute.String("id", req.ID.String()),
		),
	)
	ctx.Request = ctx.Request.WithContext(reqCtx)
	// Setup crash handler here, and also measure the timing
	defer func() {
		callInfo := slog.Group("call",
			slog.String("id", req.ID.String()),
			slog.String("method", req.Method),
			slog.Bool("IsNotification", req.Notif),
		)
		if err := recover(); err != nil {
			app.logger.ErrorContext(ctx.Request.Context(),
				"A fatal error occured while processing RPC call/notification",
				callInfo,
				slog.Any("error", err),
			)
			if req.Notif {
				res = nil
			} else {
				res = &jsonrpc2.Response{
					ID:    req.ID,
					Error: BuildJSONRPCError(jsonrpc2.CodeInternalError, "Internal error occured", nil),
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
				slog.Log(ctx.Request.Context(), level,
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
					ctx.Request.Context(),
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
		res = &jsonrpc2.Response{
			ID: req.ID,
			Error: &jsonrpc2.Error{
				Code:    jsonrpc2.CodeMethodNotFound,
				Message: fmt.Sprintf("Method %s not found", req.Method),
			},
		}
		return
	}

	var raw json.RawMessage = json.RawMessage("null")
	if req.Params != nil {
		raw = *req.Params
	}

	result, err := method(ctx, raw)
	if err != nil {
		app.logger.ErrorContext(ctx.Request.Context(), "Error occured while processing RPC", slog.String("method", req.Method), slog.Any("error", err), slog.Any("timeTaken", time.Since(rpcStart)))
		switch e := err.(type) {
		case *jsonrpc2.Error:
			res = &jsonrpc2.Response{ID: req.ID, Error: e}
			return
		default:
			res = &jsonrpc2.Response{
				ID: req.ID,
				Error: BuildJSONRPCError(
					jsonrpc2.CodeInternalError,
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
		app.logger.ErrorContext(ctx.Request.Context(), "RPC succeeded but JSON marshalling failed", slog.String("method", req.Method), slog.Any("error", err))
		res = &jsonrpc2.Response{
			ID: req.ID,
			Error: BuildJSONRPCError(
				jsonrpc2.CodeInternalError,
				"Error occured while marshalling response",
				err.Error(),
			),
		}
		return
	}

	responseJSON := json.RawMessage(responseRaw)

	res = &jsonrpc2.Response{
		ID:     req.ID,
		Result: &responseJSON,
	}
	return
}
