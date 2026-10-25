package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

const lokiReadTimeout = 15 * time.Second

// LokiReader reads one selector's lines for log drains. Selectors come from
// databaseLogSelector, never from a request.
type LokiReader struct {
	base   string
	client *http.Client
}

func NewLokiReader(base string, client *http.Client) *LokiReader {
	return &LokiReader{base: strings.TrimRight(base, "/"), client: client}
}

// maxDrainLokiAnswer bounds one read's transfer; lines are cut as they are
// decoded, so memory holds at most limit lines of MaxLineBytes.
const maxDrainLokiAnswer = 192 << 20

func (r *LokiReader) ReadLines(ctx context.Context, selector string, fromNS, toNS int64, limit int) ([]logdrain.Line, error) {
	params := url.Values{
		"query":     {selector},
		"start":     {strconv.FormatInt(fromNS, 10)},
		"end":       {strconv.FormatInt(toNS, 10)},
		"limit":     {strconv.Itoa(limit)},
		"direction": {"forward"},
	}
	var lines []logdrain.Line
	err := r.stream(ctx, "/loki/api/v1/query_range", params, func(body io.Reader) error {
		var err error
		lines, err = decodeStreamLines(json.NewDecoder(io.LimitReader(body, maxDrainLokiAnswer)))
		return err
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].TS < lines[j].TS })
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return lines, nil
}

// decodeStreamLines walks {"data":{"result":[{"values":[[ts,line]...]}]}}
// one value at a time, cutting each line before keeping it.
func decodeStreamLines(dec *json.Decoder) ([]logdrain.Line, error) {
	walker := &lineWalker{dec: dec, lines: []logdrain.Line{}}
	err := forEachKey(dec, walker.top)
	return walker.lines, err
}

type lineWalker struct {
	dec   *json.Decoder
	lines []logdrain.Line
}

func (w *lineWalker) top(key string) error {
	if key != "data" {
		return skipValue(w.dec)
	}
	return forEachKey(w.dec, w.data)
}

func (w *lineWalker) data(key string) error {
	if key != "result" {
		return skipValue(w.dec)
	}
	return forEachElement(w.dec, w.stream)
}

func (w *lineWalker) stream() error {
	return forEachKey(w.dec, w.streamField)
}

func (w *lineWalker) streamField(key string) error {
	if key != "values" {
		return skipValue(w.dec)
	}
	return forEachElement(w.dec, w.value)
}

func (w *lineWalker) value() error {
	line, ok, err := decodeValue(w.dec)
	if ok {
		w.lines = append(w.lines, line)
	}
	return err
}

func decodeValue(dec *json.Decoder) (logdrain.Line, bool, error) {
	var pair []string
	if err := dec.Decode(&pair); err != nil {
		return logdrain.Line{}, false, err
	}
	if len(pair) < 2 {
		return logdrain.Line{}, false, nil
	}
	ts, err := strconv.ParseInt(pair[0], 10, 64)
	if err != nil {
		return logdrain.Line{}, false, nil
	}
	return logdrain.Line{TS: ts, Line: logdrain.TruncateLine(pair[1])}, true, nil
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != want {
		return fmt.Errorf("loki answer: want %q", want)
	}
	return nil
}

func forEachKey(dec *json.Decoder, each func(key string) error) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := token.(string)
		if err := each(key); err != nil {
			return err
		}
	}
	return expectDelim(dec, '}')
}

func forEachElement(dec *json.Decoder, each func() error) error {
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		if err := each(); err != nil {
			return err
		}
	}
	return expectDelim(dec, ']')
}

func skipValue(dec *json.Decoder) error {
	var skipped json.RawMessage
	return dec.Decode(&skipped)
}

// CountLines counts the selector's lines in the range ending at toNS.
func (r *LokiReader) CountLines(ctx context.Context, selector string, fromNS, toNS int64) (int64, error) {
	seconds := (toNS - fromNS + int64(time.Second) - 1) / int64(time.Second)
	params := url.Values{
		"query": {fmt.Sprintf("sum(count_over_time(%s[%ds]))", selector, max(seconds, 1))},
		"time":  {strconv.FormatInt(toNS, 10)},
	}
	var body struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := r.get(ctx, "/loki/api/v1/query", params, &body); err != nil {
		return 0, err
	}
	if len(body.Data.Result) == 0 || len(body.Data.Result[0].Value) < 2 {
		return 0, nil
	}
	raw, _ := body.Data.Result[0].Value[1].(string)
	count, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("loki count: %w", err)
	}
	return int64(count), nil
}

func (r *LokiReader) get(ctx context.Context, path string, params url.Values, into any) error {
	return r.stream(ctx, path, params, func(body io.Reader) error {
		return json.NewDecoder(io.LimitReader(body, maxLokiAnswer)).Decode(into)
	})
}

func (r *LokiReader) stream(ctx context.Context, path string, params url.Values, read func(io.Reader) error) error {
	ctx, cancel := context.WithTimeout(ctx, lokiReadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("loki answered %d", resp.StatusCode)
	}
	return read(resp.Body)
}
