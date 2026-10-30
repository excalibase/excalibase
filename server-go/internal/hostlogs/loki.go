package hostlogs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const pushTimeout = 15 * time.Second

// Entry is one stored line and its time in nanoseconds.
type Entry struct {
	At   int64
	Line string
}

// Stream is one container's lines under its labels.
type Stream struct {
	Labels  Labels
	Entries []Entry
}

// LokiPusher stores streams through Loki's push API.
type LokiPusher struct {
	url    string
	client *http.Client
}

// NewPushClient is the HTTP client pushes share: one kept-alive connection.
func NewPushClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: pushTimeout,
		MaxIdleConnsPerHost:   4,
	}}
}

// NewLokiPusher pushes to the Loki at base.
func NewLokiPusher(base string, client *http.Client) *LokiPusher {
	return &LokiPusher{url: strings.TrimRight(base, "/") + "/loki/api/v1/push", client: client}
}

type pushStream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

// Push stores the streams. A 5xx, 429 or 408 is an error to retry; any other
// refusal is Loki refusing the lines for good: it is logged, not retried.
func (p *LokiPusher) Push(ctx context.Context, streams []Stream) error {
	body := struct {
		Streams []pushStream `json:"streams"`
	}{Streams: make([]pushStream, 0, len(streams))}
	for _, stream := range streams {
		values := make([][2]string, 0, len(stream.Entries))
		for _, entry := range stream.Entries {
			values = append(values, [2]string{strconv.FormatInt(entry.At, 10), entry.Line})
		}
		body.Streams = append(body.Streams, pushStream{Stream: stream.Labels.toMap(), Values: values})
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("loki push: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch status := resp.StatusCode; {
	case status == http.StatusOK || status == http.StatusNoContent:
		return nil
	case status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout:
		return fmt.Errorf("loki push answered %d", status)
	default:
		// Retrying cannot store what Loki refuses for good (too old, too long, a bad request).
		// Only the status: Loki's reason can quote the tenant's line.
		log.Printf("host logs: loki refused %d lines for good (%d)", countEntries(streams), status)
		return nil
	}
}

func countEntries(streams []Stream) int {
	total := 0
	for _, stream := range streams {
		total += len(stream.Entries)
	}
	return total
}
