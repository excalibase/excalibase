package enginestats

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxStreamLine is the longest stamped line a follow holds (16 KiB of text
// and its stamp, as the shipper keeps it); the rest of it is dropped.
const maxStreamLine = 16<<10 + 64

// followHeaderTimeout bounds the wait for the engine to start a follow.
const followHeaderTimeout = 15 * time.Second

// followClient has no overall timeout: a follow lasts as long as its context.
var followClient = &http.Client{Transport: &http.Transport{
	DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	ResponseHeaderTimeout: followHeaderTimeout,
	MaxIdleConnsPerHost:   4,
}}

// LogLine is one line a container wrote and when the engine received it.
type LogLine struct {
	At   time.Time
	Text string
}

// LineStream reads a followed container log line by line.
type LineStream struct {
	body  io.Closer
	lines *bufio.Reader
}

// FollowLogs streams a container's stdout and stderr from since (whole
// seconds, as both engines take it; zero is the start) until ctx ends or the
// container stops. The caller closes the stream.
func (c *Client) FollowLogs(ctx context.Context, ref string, since time.Time) (*LineStream, error) {
	query := url.Values{"stdout": {"1"}, "stderr": {"1"}, "follow": {"1"}, "timestamps": {"1"}}
	if !since.IsZero() {
		query.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	target := *c.base
	target.Path = "/containers/" + url.PathEscape(ref) + "/logs"
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := followClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("engine follow: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return newLineStream(resp.Body), nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNotFound
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("engine follow: answered %d", resp.StatusCode)
	}
}

func newLineStream(body io.ReadCloser) *LineStream {
	return &LineStream{body: body, lines: bufio.NewReaderSize(&demux{in: bufio.NewReader(body)}, maxStreamLine)}
}

// Close ends the follow.
func (s *LineStream) Close() error { return s.body.Close() }

// Next is the next stamped line; io.EOF when the engine ends the stream.
// Lines the engine did not stamp are skipped.
func (s *LineStream) Next() (LogLine, error) {
	for {
		raw, err := s.readLine()
		if raw != "" {
			if line, ok := parseStamped(raw); ok {
				return line, nil
			}
		}
		if err != nil {
			return LogLine{}, err
		}
	}
}

// readLine is one line without its end, cut at maxStreamLine.
func (s *LineStream) readLine() (string, error) {
	chunk, err := s.lines.ReadSlice('\n')
	line := string(chunk)
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = s.lines.ReadSlice('\n')
	}
	return strings.TrimRight(line, "\r\n"), err
}

func parseStamped(raw string) (LogLine, bool) {
	stamp, text, ok := strings.Cut(raw, " ")
	if !ok {
		return LogLine{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return LogLine{}, false
	}
	return LogLine{At: at, Text: text}, true
}

// demux strips the engine's 8-byte frame headers; a container with a terminal
// writes plain text, which passes through.
type demux struct {
	in      *bufio.Reader
	decided bool
	plain   bool
	left    int64
}

func (d *demux) Read(p []byte) (int, error) {
	if !d.decided {
		header, err := d.in.Peek(8)
		if len(header) == 0 && err != nil {
			return 0, err
		}
		d.decided = true
		d.plain = !isFrameHeader(header)
	}
	if d.plain {
		return d.in.Read(p)
	}
	for d.left == 0 {
		var header [8]byte
		if _, err := io.ReadFull(d.in, header[:]); err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		d.left = int64(binary.BigEndian.Uint32(header[4:8]))
	}
	if int64(len(p)) > d.left {
		p = p[:d.left]
	}
	n, err := d.in.Read(p)
	d.left -= int64(n)
	return n, err
}

func isFrameHeader(header []byte) bool {
	return len(header) == 8 && header[0] <= 2 && header[1] == 0 && header[2] == 0 && header[3] == 0
}
