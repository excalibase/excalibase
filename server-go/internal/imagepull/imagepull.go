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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
			return fmt.Errorf("pull %s: registry refused, not retrying: %w", ref, last)
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

// permanent is a refusal that asking again will not change, or will only make
// worse: bad credentials, a missing image, and a registry that is throttling.
func permanent(err error) bool {
	if errdefs.IsUnauthorized(err) || errdefs.IsPermissionDenied(err) || errdefs.IsInvalidArgument(err) ||
		errdefs.IsNotFound(err) || errdefs.IsResourceExhausted(err) {
		return true
	}
	// A refused socket ("permission denied") is the local daemon, not a registry.
	text := strings.ReplaceAll(strings.ToLower(err.Error()), "permission denied", "")
	for _, marker := range []string{"toomanyrequests", "too many requests", "manifest unknown", "unauthorized", "denied", "name unknown"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

// ryukImage is the reaper container testcontainers starts before the first
// container of a run; it pulls it through the same path that drops failures.
const ryukImage = "testcontainers/ryuk:0.13.0"

var ryukOnce sync.Once

// EnsureLocal is Ensure against the engine the tests will use (see dockerHost),
// for integration tests that start a container from an image the engine may
// not hold yet. The first call also pre-pulls the reaper image.
func EnsureLocal(ctx context.Context, ref string) error {
	options := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host := dockerHost(os.Getenv, os.Getenv("HOME"), pathExists); host != "" {
		options = append(options, client.WithHost(host))
	}
	engine, err := client.NewClientWithOpts(options...)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer engine.Close()
	if _, err := engine.Ping(ctx); err != nil {
		// No daemon: leave the start to the container library, which reports
		// or skips as the test already did. Said out loud so it is not a mystery.
		log.Printf("imagepull: docker daemon unreachable (%v); %s is left to testcontainers", err, ref)
		return nil
	}
	if !ryukDisabled(os.Getenv, os.Getenv("HOME")) {
		ryukOnce.Do(func() {
			if err := Ensure(ctx, engine, ryukImage, 3, 5*time.Second); err != nil {
				log.Printf("imagepull: %v", err)
			}
		})
	}
	return Ensure(ctx, engine, ref, 3, 5*time.Second)
}

// socketPath is the default daemon socket testcontainers looks for.
const socketPath = "/var/run/docker.sock"

// dockerHost is the daemon address testcontainers v0.42.0 would use, in its
// order (core.extractDockerHost): tc.host from ~/.testcontainers.properties,
// DOCKER_HOST, the default socket when it exists, then docker.host from the
// properties. Empty leaves the Docker client's own default, which is the socket.
func dockerHost(env func(string) string, home string, exists func(string) bool) string {
	properties := readProperties(home)
	switch {
	case properties["tc.host"] != "":
		return properties["tc.host"]
	case env("DOCKER_HOST") != "":
		return env("DOCKER_HOST")
	case exists(socketPath):
		return ""
	default:
		return properties["docker.host"]
	}
}

// ryukDisabled mirrors testcontainers v0.42.0 config.read: ryuk.disabled from
// the properties file, then any boolean TESTCONTAINERS_RYUK_DISABLED replaces it
// with whether the text is exactly "true" (so "1" does not disable the reaper).
func ryukDisabled(env func(string) string, home string) bool {
	disabled, _ := strconv.ParseBool(readProperties(home)["ryuk.disabled"])
	if value := env("TESTCONTAINERS_RYUK_DISABLED"); value != "" {
		if _, err := strconv.ParseBool(value); err == nil {
			disabled = value == "true"
		}
	}
	return disabled
}

// readProperties reads ~/.testcontainers.properties as key=value lines.
func readProperties(home string) map[string]string {
	properties := map[string]string{}
	body, err := os.ReadFile(filepath.Join(home, ".testcontainers.properties"))
	if err != nil {
		return properties
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if key, value, found := strings.Cut(line, "="); found && !strings.HasPrefix(line, "#") {
			properties[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return properties
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
