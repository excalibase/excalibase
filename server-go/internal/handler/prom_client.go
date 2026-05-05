package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// PromClient is a tiny Prometheus HTTP API helper. We only need instant
// queries for the admin handler's per-pod CPU/memory readout, so the surface
// is intentionally narrow — no range queries, no series API, no remote write.
//
// Implements the promQuerier interface in admin.go.
type PromClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewPromClient(baseURL string) *PromClient {
	return &PromClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// InstantValue runs an instant Prom query and returns the first scalar value
// from the result vector (or 0 if empty/error). Errors are logged via the
// caller; this is best-effort observability data, never load-bearing.
func (c *PromClient) InstantValue(ctx context.Context, query string) (float64, error) {
	if c == nil || c.baseURL == "" {
		return 0, fmt.Errorf("prom client not configured")
	}
	u, err := url.Parse(c.baseURL + "/api/v1/query")
	if err != nil {
		return 0, err
	}
	q := u.Query()
	q.Set("query", query)
	u.RawQuery = q.Encode()

	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var body struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string          `json:"resultType"`
			Result     []promResultRow `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, err
	}
	if body.Status != "success" || len(body.Data.Result) == 0 {
		return 0, nil
	}
	// Each result is [timestamp, "value"]. Skip the timestamp, parse the string.
	if len(body.Data.Result[0].Value) != 2 {
		return 0, nil
	}
	s, ok := body.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

type promResultRow struct {
	Metric map[string]string `json:"metric"`
	Value  []interface{}     `json:"value"`
}
