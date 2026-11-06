package imagepull

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func TestDrain_ReturnsTheErrorAPullStreamCarries(t *testing.T) {
	stream := `{"status":"Pulling from library/postgres"}` + "\n" +
		`{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}` + "\n"
	err := Drain(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("a stream that ended in an error must fail, got %v", err)
	}
}

func TestDrain_AcceptsACompletePull(t *testing.T) {
	stream := `{"status":"Pulling from library/postgres"}` + "\n" + `{"status":"Status: Downloaded newer image"}` + "\n"
	if err := Drain(strings.NewReader(stream)); err != nil {
		t.Fatalf("a complete pull must pass: %v", err)
	}
}

type fakeEngine struct {
	present  bool
	pulls    []string
	streams  []string
	pullErrs []error
}

func (f *fakeEngine) ImageInspect(_ context.Context, _ string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if f.present {
		return image.InspectResponse{}, nil
	}
	return image.InspectResponse{}, errNotFound{}
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	attempt := len(f.pulls)
	f.pulls = append(f.pulls, ref)
	if attempt < len(f.pullErrs) && f.pullErrs[attempt] != nil {
		return nil, f.pullErrs[attempt]
	}
	body := `{"status":"ok"}`
	if attempt < len(f.streams) {
		body = f.streams[attempt]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

type errNotFound struct{}

func (errNotFound) Error() string { return "No such image" }
func (errNotFound) NotFound()     {}

func TestEnsure_SkipsAPresentImage(t *testing.T) {
	engine := &fakeEngine{present: true}
	if err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(engine.pulls) != 0 {
		t.Fatalf("pulled an image the engine holds: %v", engine.pulls)
	}
}

func TestEnsure_RetriesAPullThatFailedInTheStream(t *testing.T) {
	engine := &fakeEngine{streams: []string{`{"error":"net/http: TLS handshake timeout"}`, `{"status":"ok"}`}}
	if err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond); err != nil {
		t.Fatalf("the second attempt succeeded: %v", err)
	}
	if len(engine.pulls) != 2 {
		t.Fatalf("pulls = %d, want 2", len(engine.pulls))
	}
}

func TestEnsure_GivesUpAfterTheAttempts(t *testing.T) {
	engine := &fakeEngine{pullErrs: []error{errors.New("denied"), errors.New("denied"), errors.New("denied")}}
	err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("want the last pull error, got %v", err)
	}
	if len(engine.pulls) != 3 {
		t.Fatalf("pulls = %d, want 3", len(engine.pulls))
	}
}
