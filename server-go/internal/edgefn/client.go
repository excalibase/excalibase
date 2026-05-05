package edgefn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const errCreateRequest = "create request: %w"


// DeployRequest is the payload the platform sends to /deploy on the Deno runtime.
// Code is the already-bundled JS source; Secrets are merged user + built-in env vars.
type DeployRequest struct {
	ID      string            `json:"id"`
	Code    string            `json:"code"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// InvokeRequest is forwarded to the runtime on /invoke/{id}. The runtime
// reconstructs a Fetch API Request from these fields and calls the user's
// default export.
type InvokeRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// InvokeResponse is what the runtime returns — the user handler's Response
// serialized as status + headers + body.
type InvokeResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// LogEntry is a single captured console.* line from user code, as returned
// by GET /logs/{id} on the runtime. Timestamps are unix-ms.
type LogEntry struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
	TS    int64  `json:"ts"`
}

// RuntimeClient communicates with the shared Deno runtime over HTTP.
type RuntimeClient struct {
	baseURL string
	secret  string
	http    *http.Client
}

func NewRuntimeClient(baseURL, secret string) *RuntimeClient {
	return &RuntimeClient{
		baseURL: baseURL,
		secret:  secret,
		http:    &http.Client{Timeout: 35 * time.Second},
	}
}

func (c *RuntimeClient) setHeaders(req *http.Request) {
	if c.secret != "" {
		req.Header.Set("X-Runtime-Secret", c.secret)
	}
	req.Header.Set("Content-Type", "application/json")
}

func (c *RuntimeClient) Health(ctx context.Context) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return false, fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200, nil
}

// Deploy registers or replaces a function in the runtime. The runtime
// instantiates a fresh Deno Worker with the supplied code + secrets env.
func (c *RuntimeClient) Deploy(ctx context.Context, deployReq DeployRequest) error {
	body, err := json.Marshal(deployReq)
	if err != nil {
		return fmt.Errorf("marshal deploy body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/deploy", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("deploy %s: %w", deployReq.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("deploy %s: status %d: %s", deployReq.ID, resp.StatusCode, string(b))
	}
	return nil
}

// Invoke calls the function with the given Request shape and returns the
// Response shape. Caller is responsible for forwarding it to the end user.
func (c *RuntimeClient) Invoke(ctx context.Context, id string, invokeReq InvokeRequest) (*InvokeResponse, error) {
	body, err := json.Marshal(invokeReq)
	if err != nil {
		return nil, fmt.Errorf("marshal invoke body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/invoke/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("invoke %s: status %d: %s", id, resp.StatusCode, string(b))
	}
	var result InvokeResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("invoke %s: decode response: %w", id, err)
	}
	return &result, nil
}

func (c *RuntimeClient) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/delete/"+url.PathEscape(id), nil)
	if err != nil {
		return fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	defer resp.Body.Close()
	return nil
}

// Logs fetches the ring buffer for a function from the runtime. If sinceMs
// is non-zero, only entries strictly newer than that timestamp are returned.
// A non-existent function id returns ErrLogsNotFound.
func (c *RuntimeClient) Logs(ctx context.Context, id string, sinceMs int64) ([]LogEntry, error) {
	endpoint := c.baseURL + "/logs/" + url.PathEscape(id)
	if sinceMs > 0 {
		endpoint += "?since=" + fmt.Sprintf("%d", sinceMs)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrLogsNotFound
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch logs: %d: %s", resp.StatusCode, string(body))
	}
	var result struct {
		Logs []LogEntry `json:"logs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode logs: %w", err)
	}
	return result.Logs, nil
}

// ErrLogsNotFound is returned when the runtime has no record of the function
// id (either never deployed, or the runtime pod restarted and dropped it).
var ErrLogsNotFound = fmt.Errorf("function not found in runtime")

func (c *RuntimeClient) List(ctx context.Context) ([]map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/scripts", nil)
	if err != nil {
		return nil, fmt.Errorf(errCreateRequest, err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Scripts []map[string]interface{} `json:"scripts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("list scripts: decode response: %w", err)
	}
	return result.Scripts, nil
}
