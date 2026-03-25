package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// IDGenerator defines the interface for generating request IDs
type IDGenerator interface {
	NextID() jsonrpc2.ID
}

type defaultIDGenerator struct {
	counter int64
}

func (g *defaultIDGenerator) NextID() jsonrpc2.ID {
	id := atomic.AddInt64(&g.counter, 1)
	return jsonrpc2.ID{Num: uint64(id), IsString: false}
}

type Client struct {
	url         string
	httpClient  *http.Client
	headers     http.Header
	idGenerator IDGenerator
}

type ClientOption func(*Client)

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

func WithHeader(key, value string) ClientOption {
	return func(c *Client) {
		if c.headers == nil {
			c.headers = make(http.Header)
		}
		c.headers.Add(key, value)
	}
}

func WithIDGenerator(gen IDGenerator) ClientOption {
	return func(c *Client) {
		c.idGenerator = gen
	}
}

func NewClient(url string, opts ...ClientOption) *Client {
	c := &Client{
		url:         url,
		httpClient:  http.DefaultClient,
		headers:     make(http.Header),
		idGenerator: &defaultIDGenerator{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) sendRequest(ctx context.Context, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		req.Header[k] = v
	}

	// Inject OTel propagation
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return json.RawMessage(respBody), nil
}

// Call performs a type-safe JSON-RPC call
func Call[P any, R any](ctx context.Context, c *Client, method string, params P) (R, error) {
	var result R
	id := c.idGenerator.NextID()

	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return result, fmt.Errorf("marshal params: %w", err)
	}
	paramsJSON := json.RawMessage(paramsRaw)

	req := &jsonrpc2.Request{
		Method: method,
		Params: &paramsJSON,
		ID:     id,
	}

	respRaw, err := c.sendRequest(ctx, req)
	if err != nil {
		return result, err
	}

	var resp jsonrpc2.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		return result, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.ID.String() != id.String() {
		return result, fmt.Errorf("response ID mismatch: expected %s, got %s", id.String(), resp.ID.String())
	}

	if resp.Error != nil {
		return result, resp.Error
	}

	if resp.Result == nil {
		return result, nil
	}

	if err := json.Unmarshal(*resp.Result, &result); err != nil {
		return result, fmt.Errorf("unmarshal result: %w", err)
	}

	return result, nil
}

// Notify performs a JSON-RPC notification (no response expected)
func Notify[P any](ctx context.Context, c *Client, method string, params P) error {
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	paramsJSON := json.RawMessage(paramsRaw)

	req := &jsonrpc2.Request{
		Method: method,
		Params: &paramsJSON,
		Notif:  true,
	}

	_, err = c.sendRequest(ctx, req)
	return err
}

// Batch represents a builder for JSON-RPC batch requests
type Batch struct {
	client *Client
	calls  []batchCallEntry
}

type batchCallEntry struct {
	request *jsonrpc2.Request
	handle  func(*jsonrpc2.Response)
}

func (c *Client) NewBatch() *Batch {
	return &Batch{client: c}
}

type BatchCall[R any] struct {
	result R
	err    error
	done   chan struct{}
	once   sync.Once
}

func (bc *BatchCall[R]) Wait() (R, error) {
	<-bc.done
	return bc.result, bc.err
}

func (bc *BatchCall[R]) set(result R, err error) {
	bc.once.Do(func() {
		bc.result = result
		bc.err = err
		close(bc.done)
	})
}

func AddBatchCall[P any, R any](b *Batch, method string, params P) (*BatchCall[R], error) {
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	paramsJSON := json.RawMessage(paramsRaw)
	id := b.client.idGenerator.NextID()

	req := &jsonrpc2.Request{
		Method: method,
		Params: &paramsJSON,
		ID:     id,
	}

	res := &BatchCall[R]{
		done: make(chan struct{}),
	}

	b.calls = append(b.calls, batchCallEntry{
		request: req,
		handle: func(resp *jsonrpc2.Response) {
			if resp == nil {
				res.set(res.result, errors.New("no response"))
				return
			}
			if resp.ID.String() != id.String() {
				res.set(res.result, fmt.Errorf("response ID mismatch: expected %s, got %s", id.String(), resp.ID.String()))
				return
			}
			if resp.Error != nil {
				res.set(res.result, resp.Error)
				return
			}
			if resp.Result != nil {
				var r R
				if err := json.Unmarshal(*resp.Result, &r); err != nil {
					res.set(res.result, err)
				} else {
					res.set(r, nil)
				}
			} else {
				res.set(res.result, nil)
			}
		},
	})

	return res, nil
}

func AddBatchNotification[P any](b *Batch, method string, params P) error {
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	paramsJSON := json.RawMessage(paramsRaw)

	req := &jsonrpc2.Request{
		Method: method,
		Params: &paramsJSON,
		Notif:  true,
	}

	b.calls = append(b.calls, batchCallEntry{
		request: req,
		handle:  nil,
	})

	return nil
}

func (b *Batch) Execute(ctx context.Context) error {
	if len(b.calls) == 0 {
		return nil
	}

	reqs := make([]*jsonrpc2.Request, len(b.calls))
	for i, c := range b.calls {
		reqs[i] = c.request
	}

	respRaw, err := b.client.sendRequest(ctx, reqs)
	if err != nil {
		// If the entire batch fails, set error on all calls
		for _, c := range b.calls {
			if c.handle != nil {
				c.handle(&jsonrpc2.Response{
					Error: &jsonrpc2.Error{Code: jsonrpc2.CodeInternalError, Message: err.Error()},
				})
			}
		}
		return err
	}

	if respRaw == nil {
		// All were notifications or empty response
		for _, c := range b.calls {
			if c.handle != nil {
				c.handle(&jsonrpc2.Response{})
			}
		}
		return nil
	}

	var responses []jsonrpc2.Response
	if err := json.Unmarshal(respRaw, &responses); err != nil {
		// If unmarshal fails, it might be a single response (error) or just invalid
		var singleResp jsonrpc2.Response
		if err2 := json.Unmarshal(respRaw, &singleResp); err2 == nil {
			responses = []jsonrpc2.Response{singleResp}
		} else {
			for _, c := range b.calls {
				if c.handle != nil {
					c.handle(&jsonrpc2.Response{
						Error: &jsonrpc2.Error{Code: jsonrpc2.CodeInternalError, Message: "failed to unmarshal batch response: " + err.Error()},
					})
				}
			}
			return fmt.Errorf("unmarshal batch response: %w", err)
		}
	}

	respMap := make(map[string]*jsonrpc2.Response)
	for i := range responses {
		respMap[responses[i].ID.String()] = &responses[i]
	}

	for _, c := range b.calls {
		if c.handle == nil {
			continue // Notification
		}
		resp := respMap[c.request.ID.String()]
		c.handle(resp)
	}

	return nil
}
