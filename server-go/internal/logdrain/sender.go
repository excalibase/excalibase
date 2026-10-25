package logdrain

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// Batch bounds, enforced by provisioning when it builds a batch and again here.
const (
	MaxBatchLines = 500
	MaxBatchBytes = 1 << 20
	// MaxLineBytes keeps one line well under Datadog's 1 MB per-log limit.
	MaxLineBytes = 16 << 10
)

const (
	// A destination that cannot take a batch within these is failing: slow
	// answers must not hold the forwarder's workers.
	sendTimeout     = 15 * time.Second
	headerTimeout   = 10 * time.Second
	tlsTimeout      = 5 * time.Second
	maxAnswerToRead = 4 << 10
	datadogSource   = "excalibase"
	httpsSource     = "database"
)

// Line is one log line and its Loki timestamp in nanoseconds.
type Line struct {
	TS   int64  `json:"ts,string"`
	Line string `json:"line"`
}

// Batch is the lines of one drain between two cursors, with what the
// forwarder needs to deliver them. Secret never leaves process memory.
type Batch struct {
	DrainID    string `json:"drainId"`
	ProjectID  string `json:"projectId"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	HeaderName string `json:"headerName,omitempty"`
	Secret     string `json:"secret,omitempty"`
	From       int64  `json:"from,string"`
	To         int64  `json:"to,string"`
	Lines      []Line `json:"lines"`
}

// SendError is a delivery failure described without the URL, key or the
// destination's answer.
type SendError struct{ msg string }

func (e *SendError) Error() string { return e.msg }

func sendFailure(format string, args ...any) error {
	return &SendError{msg: fmt.Sprintf(format, args...)}
}

// NewHTTPClient reaches destinations only through dialer, never through a
// proxy, and never follows a redirect.
func NewHTTPClient(dialer *publicaddr.Dialer) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsTimeout,
		ResponseHeaderTimeout: headerTimeout,
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: sendTimeout, CheckRedirect: refuseRedirect}
}

func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// Sender POSTs batches.
type Sender struct {
	client *http.Client
	// targetCheck re-validates the destination before anything is sent.
	targetCheck func(string) error
	// datadogTarget admits the hosts a Datadog key may be sent to.
	datadogTarget func(string) bool
}

// NewSender never follows a redirect, whatever client it is given.
func NewSender(client *http.Client) *Sender {
	noRedirects := *client
	noRedirects.CheckRedirect = refuseRedirect
	return &Sender{client: &noRedirects, datadogTarget: IsDatadogIntake, targetCheck: func(raw string) error {
		_, err := ParseEndpoint(raw)
		return err
	}}
}

// Send delivers one batch; any non-2xx answer is a failure.
func (s *Sender) Send(ctx context.Context, batch Batch) error {
	if batch.Kind != KindDatadog && batch.Kind != KindHTTPS {
		return sendFailure("unknown drain kind")
	}
	if err := s.targetCheck(batch.Target); err != nil {
		return sendFailure("the destination is not an https URL on a public host")
	}
	if batch.Kind == KindDatadog && !s.datadogTarget(batch.Target) {
		return sendFailure("the destination is not a Datadog logs intake")
	}
	body, err := encode(batch)
	if err != nil {
		return sendFailure("the batch could not be encoded")
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, batch.Target, bytes.NewReader(body))
	if err != nil {
		return sendFailure("the destination URL is not usable")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "excalibase-log-forwarder")
	if batch.Kind == KindDatadog {
		req.Header.Set("DD-API-KEY", batch.Secret)
	} else if batch.HeaderName != "" {
		req.Header.Set(batch.HeaderName, batch.Secret)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return describeTransportError(err)
	}
	defer resp.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswerToRead))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300 && readErr != nil:
		return sendFailure("the destination did not finish its answer in time")
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return sendFailure("the destination answered %d, a redirect, which is not followed", resp.StatusCode)
	default:
		return sendFailure("the destination answered %d", resp.StatusCode)
	}
}

// describeTransportError names the cause only: net/http's own error text
// carries the full URL, which may hold a token.
func describeTransportError(err error) error {
	var netErr net.Error
	var certErr *tls.CertificateVerificationError
	switch {
	case errors.Is(err, publicaddr.ErrInternalAddress):
		return sendFailure("the destination is not a public address")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return sendFailure("the destination did not answer in time")
	case errors.As(err, &certErr):
		return sendFailure("the destination's TLS certificate was not accepted")
	case strings.Contains(err.Error(), "did not resolve"), isDNSError(err):
		return sendFailure("the destination's name did not resolve")
	default:
		return sendFailure("could not connect to the destination")
	}
}

func isDNSError(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

type datadogEntry struct {
	Message   string `json:"message"`
	Source    string `json:"ddsource"`
	Service   string `json:"service"`
	Tags      string `json:"ddtags"`
	Timestamp int64  `json:"timestamp"`
}

type httpsEntry struct {
	Timestamp string `json:"timestamp"`
	Message   string `json:"message"`
	Project   string `json:"project"`
	Source    string `json:"source"`
}

// encode renders the batch as the JSON array the destination takes, without
// HTML escaping so its size is what EncodedSize measured.
func encode(batch Batch) ([]byte, error) {
	var entries any
	if batch.Kind == KindDatadog {
		list := make([]datadogEntry, 0, len(batch.Lines))
		for _, line := range batch.Lines {
			list = append(list, datadogEntry{Message: TruncateLine(line.Line), Source: datadogSource,
				Service: batch.ProjectID, Tags: "project:" + batch.ProjectID, Timestamp: line.TS / int64(time.Millisecond)})
		}
		entries = list
	} else {
		list := make([]httpsEntry, 0, len(batch.Lines))
		for _, line := range batch.Lines {
			list = append(list, httpsEntry{Timestamp: time.Unix(0, line.TS).UTC().Format(time.RFC3339Nano),
				Message: TruncateLine(line.Line), Project: batch.ProjectID, Source: httpsSource})
		}
		entries = list
	}
	return marshalPlain(entries)
}

func marshalPlain(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// EncodedSize is the bytes line takes as a JSON string.
func EncodedSize(line string) int {
	encoded, err := marshalPlain(line)
	if err != nil {
		return len(line) * 6
	}
	return len(encoded)
}

// TruncateLine cuts a line to MaxLineBytes on a rune boundary.
func TruncateLine(line string) string {
	if len(line) <= MaxLineBytes {
		return line
	}
	cut := MaxLineBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	// A copy, so the cut line does not keep the whole original alive.
	return strings.Clone(line[:cut])
}
