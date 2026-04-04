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

// RuntimeClient communicates with the Deno runtime server via HTTP.
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
		return false, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200, nil
}

func (c *RuntimeClient) Deploy(ctx context.Context, id, code string) error {
	body, err := json.Marshal(map[string]string{"id": id, "code": code})
	if err != nil {
		return fmt.Errorf("marshal deploy body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/deploy", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("deploy %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("deploy %s: status %d: %s", id, resp.StatusCode, string(b))
	}
	return nil
}

func (c *RuntimeClient) Invoke(ctx context.Context, id string, data interface{}) (interface{}, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal invoke body: %w", err)
	}
	// PathEscape prevents URL injection via crafted IDs
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/invoke/"+url.PathEscape(id), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("invoke %s: status %d: %s", id, resp.StatusCode, string(b))
	}

	var result interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("invoke %s: decode response: %w", id, err)
	}
	return result, nil
}

func (c *RuntimeClient) Delete(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/delete/"+url.PathEscape(id), nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	defer resp.Body.Close()
	return nil
}

func (c *RuntimeClient) List(ctx context.Context) ([]map[string]interface{}, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/scripts", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
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
