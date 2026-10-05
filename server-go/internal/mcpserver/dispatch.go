package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/go-chi/chi/v5"
)

// maxReplyBytes bounds what one internal call may hand back to a tool.
const maxReplyBytes = 8 << 20

// RouteError is a non-2xx answer from the route a tool called, in the route's
// own words.
type RouteError struct {
	Status  int
	Message string
}

func (e *RouteError) Error() string {
	return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status)
}

// request is one in-process call into the router.
type request struct {
	method string
	path   string
	query  url.Values
	body   any
	header http.Header
}

// dispatcher sends in-process requests through the router as the caller.
type dispatcher struct {
	router http.Handler
	caller Caller
}

func (d dispatcher) send(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return d.do(ctx, request{method: method, path: path, query: query, body: body}, out)
}

// do runs req through the router. The caller's user and narrowed token ride
// the context and no raw credential is attached, so ExtractAuth leaves them
// as they are and every gate judges the narrowed token.
func (d dispatcher) do(ctx context.Context, req request, out any) error {
	httpReq, err := d.build(ctx, req)
	if err != nil {
		return err
	}
	reply := newBufferedReply()
	d.router.ServeHTTP(reply, httpReq)
	if reply.overflow {
		return fmt.Errorf("the answer from %s is larger than %d bytes", req.path, maxReplyBytes)
	}
	if reply.status == 0 {
		reply.status = http.StatusOK
	}
	if reply.status < 200 || reply.status > 299 {
		return &RouteError{Status: reply.status, Message: errorMessage(reply.body.Bytes(), reply.status)}
	}
	if out == nil || reply.body.Len() == 0 {
		return nil
	}
	if err := json.Unmarshal(reply.body.Bytes(), out); err != nil {
		return fmt.Errorf("read the answer from %s: %w", req.path, err)
	}
	return nil
}

func (d dispatcher) build(ctx context.Context, req request) (*http.Request, error) {
	var body io.Reader = http.NoBody
	if req.body != nil {
		encoded, err := json.Marshal(req.body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	target := req.path
	if len(req.query) > 0 {
		target += "?" + req.query.Encode()
	}
	// The /mcp request's own routing state must not steer the inner request.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	ctx = auth.SetToken(auth.SetUser(ctx, d.caller.User), d.caller.Token)
	httpReq, err := http.NewRequestWithContext(ctx, req.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for name, values := range req.header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.RemoteAddr = net.JoinHostPort(d.caller.ClientAddr, "0")
	return httpReq, nil
}

// errorMessage pulls the route's refusal text out of its body.
func errorMessage(body []byte, status int) string {
	var shaped struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &shaped) == nil {
		if shaped.Error != "" {
			return shaped.Error
		}
		if shaped.Message != "" {
			return shaped.Message
		}
	}
	if text := strings.TrimSpace(string(body)); text != "" && len(text) < 500 {
		return text
	}
	return http.StatusText(status)
}

// bufferedReply is the ResponseWriter an in-process call writes into.
type bufferedReply struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func newBufferedReply() *bufferedReply {
	return &bufferedReply{header: http.Header{}}
}

func (b *bufferedReply) Header() http.Header { return b.header }

func (b *bufferedReply) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedReply) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	if b.body.Len()+len(p) > maxReplyBytes {
		b.overflow = true
		return 0, fmt.Errorf("reply larger than %d bytes", maxReplyBytes)
	}
	return b.body.Write(p)
}
