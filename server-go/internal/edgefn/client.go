package edgefn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RuntimeClient communicates with the Deno runtime server via HTTP.
type RuntimeClient struct {
	baseURL string
	http    *http.Client
}

func NewRuntimeClient(baseURL string) *RuntimeClient {
	return &RuntimeClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *RuntimeClient) Health(ctx context.Context) (bool, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200, nil
}

func (c *RuntimeClient) Deploy(ctx context.Context, id, code string) error {
	body, _ := json.Marshal(map[string]string{"id": id, "code": code})
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/deploy", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("deploy %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("deploy %s: status %d: %s", id, resp.StatusCode, string(b))
	}
	return nil
}

func (c *RuntimeClient) Invoke(ctx context.Context, id string, data interface{}) (interface{}, error) {
	body, _ := json.Marshal(data)
	req, _ := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/invoke/"+id, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("invoke %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("invoke %s: status %d: %s", id, resp.StatusCode, string(b))
	}

	var result interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	return result, nil
}

func (c *RuntimeClient) Delete(ctx context.Context, id string) error {
	req, _ := http.NewRequestWithContext(ctx, "DELETE", c.baseURL+"/delete/"+id, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete %s: %w", id, err)
	}
	defer resp.Body.Close()
	return nil
}

func (c *RuntimeClient) List(ctx context.Context) ([]map[string]interface{}, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/scripts", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Scripts []map[string]interface{} `json:"scripts"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Scripts, nil
}
