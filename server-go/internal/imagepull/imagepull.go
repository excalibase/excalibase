// Package imagepull pulls container images and reports a pull that failed
// part-way. The engine answers an image pull with 200 and a JSON stream; a
// failure after the first byte (a dropped blob, a full disk) arrives as an
// "error" message inside that stream, not as an error from the call. A caller
// that only copies the stream to the bin goes on to create a container from an
// image that is not there: "No such image".
package imagepull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/containerd/errdefs"

	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// Drain reads a pull's progress stream to its end and returns the error the
// stream carries, if any.
func Drain(stream io.Reader) error {
	decoder := json.NewDecoder(stream)
	for {
		var message struct {
			Error       string `json:"error"`
			ErrorDetail struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read pull stream: %w", err)
		}
		if text := firstNonEmpty(message.Error, message.ErrorDetail.Message); text != "" {
			return fmt.Errorf("pull stream: %s", text)
		}
	}
}

// Engine is the part of the Docker client a pull needs.
type Engine interface {
	ImageInspect(ctx context.Context, imageID string, opts ...client.ImageInspectOption) (image.InspectResponse, error)
	ImagePull(ctx context.Context, ref string, options image.PullOptions) (io.ReadCloser, error)
}

// Ensure makes sure the engine holds ref, pulling it when it does not and
// trying up to attempts times, waiting pause between tries. A registry that
// resets a connection is outside our code, so only the pull is retried.
func Ensure(ctx context.Context, engine Engine, ref string, attempts int, pause time.Duration) error {
	if _, err := engine.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	var last, previous error
	for attempt := 1; attempt <= attempts; attempt++ {
		if last = pullOnce(ctx, engine, ref); last == nil {
			if attempt > 1 {
				log.Printf("imagepull: %s pulled on attempt %d after: %v", ref, attempt, previous)
			}
			return nil
		}
		previous = last
		if permanent(last) {
			return fmt.Errorf("pull %s: %w", ref, last)
		}
		if attempt == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("pull %s: %w", ref, ctx.Err())
		case <-time.After(pause):
		}
	}
	return fmt.Errorf("pull %s after %d attempts: %w", ref, attempts, last)
}

// permanent is a refusal that asking again will not change.
func permanent(err error) bool {
	return errdefs.IsUnauthorized(err) || errdefs.IsPermissionDenied(err) || errdefs.IsInvalidArgument(err) || errdefs.IsNotFound(err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func pullOnce(ctx context.Context, engine Engine, ref string) error {
	stream, err := engine.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer stream.Close()
	return Drain(stream)
}

// EnsureLocal is Ensure against the engine the environment names
// (DOCKER_HOST, else the default socket), for integration tests that start a
// container from an image the engine may not hold yet.
func EnsureLocal(ctx context.Context, ref string) error {
	engine, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer engine.Close()
	if _, err := engine.Ping(ctx); err != nil {
		// No daemon: leave it to the container library, which reports or skips
		// as the test already did.
		return nil
	}
	return Ensure(ctx, engine, ref, 3, 5*time.Second)
}
