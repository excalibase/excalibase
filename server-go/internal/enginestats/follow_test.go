package enginestats

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func readAll(t *testing.T, stream *LineStream) []LogLine {
	t.Helper()
	var lines []LogLine
	for {
		line, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return lines
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		lines = append(lines, line)
	}
}

// EXC-672: the shipper follows a container's output with the engine's own timestamps.
func TestFollowLogsAsksForTimestampsFromTheCursorSecond(t *testing.T) {
	client, seen := engine(t, map[string]string{"/containers/abc/logs": frame(1, "2026-10-30T01:02:03.000000004Z LOG:  ready\n")})
	stream, err := client.FollowLogs(context.Background(), "abc", time.Unix(1761700000, 999))
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	lines := readAll(t, stream)
	if len(lines) != 1 || lines[0].Text != "LOG:  ready" || lines[0].At.UnixNano() != time.Date(2026, 10, 30, 1, 2, 3, 4, time.UTC).UnixNano() {
		t.Errorf("lines = %+v", lines)
	}
	if got := (*seen)[0]; got != "GET /containers/abc/logs?follow=1&since=1761700000&stderr=1&stdout=1&timestamps=1" {
		t.Errorf("request = %s", got)
	}
}

func TestFollowLogsWithoutACursorAsksFromTheStart(t *testing.T) {
	client, seen := engine(t, map[string]string{"/containers/abc/logs": ""})
	stream, err := client.FollowLogs(context.Background(), "abc", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if got := (*seen)[0]; got != "GET /containers/abc/logs?follow=1&stderr=1&stdout=1&timestamps=1" {
		t.Errorf("request = %s", got)
	}
}

// Frames split a line anywhere and carry several lines; both streams interleave.
func TestLineStreamJoinsFramesIntoLines(t *testing.T) {
	body := frame(1, "2026-10-30T00:00:01Z first ") + frame(1, "half\n2026-10-30T00:00:02Z second\n") +
		frame(2, "2026-10-30T00:00:03.5Z FATAL: third\r\n") + frame(1, "2026-10-30T00:00:04Z no newline at the end")
	stream := newLineStream(io.NopCloser(strings.NewReader(body)))
	lines := readAll(t, stream)
	want := []string{"first half", "second", "FATAL: third", "no newline at the end"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %+v", lines)
	}
	for i, text := range want {
		if lines[i].Text != text {
			t.Errorf("line %d = %q, want %q", i, lines[i].Text, text)
		}
	}
	if lines[2].At.UnixNano() != time.Date(2026, 10, 30, 0, 0, 3, 5e8, time.UTC).UnixNano() {
		t.Errorf("third at %v", lines[2].At)
	}
}

func TestLineStreamOfATerminalContainerIsPlainText(t *testing.T) {
	stream := newLineStream(io.NopCloser(strings.NewReader("2026-10-30T00:00:01Z one\n2026-10-30T00:00:02Z two\n")))
	lines := readAll(t, stream)
	if len(lines) != 2 || lines[0].Text != "one" || lines[1].Text != "two" {
		t.Errorf("lines = %+v", lines)
	}
}

// A line the engine did not stamp cannot be ordered: it is skipped, not guessed.
func TestLineStreamSkipsUnstampedLines(t *testing.T) {
	stream := newLineStream(io.NopCloser(strings.NewReader(frame(1, "not a time\n2026-10-30T00:00:01Z kept\n"))))
	lines := readAll(t, stream)
	if len(lines) != 1 || lines[0].Text != "kept" {
		t.Errorf("lines = %+v", lines)
	}
}

// A huge line is cut, never held whole in memory.
func TestLineStreamCutsAHugeLine(t *testing.T) {
	huge := "2026-10-30T00:00:01Z " + strings.Repeat("x", 3*maxStreamLine) + "\n2026-10-30T00:00:02Z after\n"
	stream := newLineStream(io.NopCloser(strings.NewReader(frame(1, huge))))
	lines := readAll(t, stream)
	if len(lines) != 2 || len(lines[0].Text) > maxStreamLine || lines[1].Text != "after" {
		t.Fatalf("got %d lines, first %d bytes", len(lines), len(lines[0].Text))
	}
}

func TestFollowLogsOfAMissingContainerIsNotFound(t *testing.T) {
	client, _ := engine(t, map[string]string{})
	if _, err := client.FollowLogs(context.Background(), "gone", time.Time{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

// A follow outlives the client's 15 s call timeout; only its context ends it.
func TestFollowLogsEndsWithItsContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(frame(1, "2026-10-30T00:00:01Z one\n")))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client, _ := New("tcp://" + strings.TrimPrefix(server.URL, "http://"))
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.FollowLogs(ctx, "abc", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if line, err := stream.Next(); err != nil || line.Text != "one" {
		t.Fatalf("first = %+v %v", line, err)
	}
	cancel()
	if _, err := stream.Next(); err == nil {
		t.Error("the stream outlived its context")
	}
}
