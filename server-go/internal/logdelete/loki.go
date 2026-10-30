package logdelete

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	lokiTimeout = 15 * time.Second
	// maxStatusAnswer bounds Loki's list of delete requests, one per deleted project.
	maxStatusAnswer = 16 << 20
)

// Status is what Loki says of one delete request.
type Status int

const (
	// StatusMissing: Loki holds no such request.
	StatusMissing Status = iota
	// StatusReceived: held, lines not yet removed.
	StatusReceived
	// StatusProcessed: the lines are gone.
	StatusProcessed
)

// LokiClient calls Loki's log deletion API.
type LokiClient struct {
	base   string
	client *http.Client
}

// NewLokiClient talks to the Loki at base.
func NewLokiClient(base string, client *http.Client) *LokiClient {
	return &LokiClient{base: strings.TrimRight(base, "/") + "/loki/api/v1/delete", client: client}
}

// Request asks Loki to delete the selector's lines between start and end.
func (c *LokiClient) Request(ctx context.Context, selector string, start, end time.Time) error {
	if err := CheckSelector(selector); err != nil {
		return err
	}
	params := url.Values{
		"query": {selector},
		"start": {strconv.FormatInt(start.Unix(), 10)},
		"end":   {strconv.FormatInt(end.Unix(), 10)},
	}
	resp, err := c.do(ctx, http.MethodPost, c.base+"?"+params.Encode())
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		// Only the status: Loki's answer repeats the request.
		return fmt.Errorf("loki delete answered %d", resp.StatusCode)
	}
	return nil
}

type deleteRequest struct {
	Query     string  `json:"query"`
	StartTime float64 `json:"start_time"`
	Status    string  `json:"status"`
}

// Status finds the request for selector from start: processed only when
// every part Loki split it into is.
func (c *LokiClient) Status(ctx context.Context, selector string, start time.Time) (Status, error) {
	if err := CheckSelector(selector); err != nil {
		return StatusMissing, err
	}
	resp, err := c.do(ctx, http.MethodGet, c.base)
	if err != nil {
		return StatusMissing, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return StatusMissing, fmt.Errorf("loki delete list answered %d", resp.StatusCode)
	}
	var requests []deleteRequest
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxStatusAnswer)).Decode(&requests); err != nil {
		return StatusMissing, fmt.Errorf("loki delete list: %w", err)
	}
	found, processed := false, true
	for _, request := range requests {
		if request.Query != selector || int64(math.Floor(request.StartTime)) != start.Unix() {
			continue
		}
		found = true
		processed = processed && request.Status == "processed"
	}
	switch {
	case !found:
		return StatusMissing, nil
	case processed:
		return StatusProcessed, nil
	default:
		return StatusReceived, nil
	}
}

func (c *LokiClient) do(ctx context.Context, method, target string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, lokiTimeout)
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("loki delete: %w", err)
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose ends the request's deadline once its body is read.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}
