// Package enginestats reads container state and one-shot stats from the
// Docker or Podman API, through the engine proxy (EXC-670). Paths carry no
// API version, so each engine answers in its own current version.
package enginestats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxAnswer bounds one answer; a stats document is a few kilobytes.
const maxAnswer = 4 << 20

const managedFilter = `{"label":["excalibase.managed=true"]}`

// ErrNotFound is a container the engine does not have.
var ErrNotFound = errors.New("no such container")

// ErrNotRunning is a stats call on a container that is not running.
var ErrNotRunning = errors.New("container is not running")

// Container is a container's identity and state.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string
	Health string
	Labels map[string]string
}

// Stats is one sample's cumulative counters and gauges.
type Stats struct {
	Read             time.Time
	CPUNanos         uint64
	OnlineCPUs       int
	MemoryBytes      uint64
	MemoryLimitBytes uint64
	NetOK            bool
	NetRx, NetTx     uint64
}

// Client talks to the engine proxy.
type Client struct {
	base *url.URL
	http *http.Client
}

// New reaches the engine at DOCKER_HOST, which is the proxy's tcp address.
func New(dockerHost string) (*Client, error) {
	address, err := url.Parse(dockerHost)
	if err != nil || address.Host == "" || (address.Scheme != "tcp" && address.Scheme != "http") {
		return nil, fmt.Errorf("engine stats: DOCKER_HOST %q is not the engine proxy's tcp:// address", dockerHost)
	}
	return &Client{
		base: &url.URL{Scheme: "http", Host: address.Host},
		http: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// Managed lists every container provisioning created, running or not.
func (c *Client) Managed(ctx context.Context) ([]Container, error) {
	var listed []struct {
		ID     string `json:"Id"`
		Names  []string
		Image  string
		State  string
		Status string
		Labels map[string]string
	}
	query := url.Values{"all": {"1"}, "filters": {managedFilter}}
	if err := c.get(ctx, "/containers/json", query, &listed); err != nil {
		return nil, err
	}
	containers := make([]Container, 0, len(listed))
	for _, entry := range listed {
		name := ""
		if len(entry.Names) > 0 {
			name = strings.TrimPrefix(entry.Names[0], "/")
		}
		containers = append(containers, Container{
			ID: entry.ID, Name: name, Image: entry.Image, State: entry.State,
			Health: healthFromStatus(entry.Status), Labels: entry.Labels,
		})
	}
	return containers, nil
}

// healthFromStatus reads Docker's "Up 3 minutes (unhealthy)"; Podman's list
// leaves the health out, so callers inspect a running container too.
func healthFromStatus(status string) string {
	for _, health := range []string{"unhealthy", "healthy", "starting"} {
		if strings.Contains(status, "("+health+")") || strings.Contains(status, "(health: "+health+")") {
			return health
		}
	}
	return ""
}

// Inspect reads one container's state and health.
func (c *Client) Inspect(ctx context.Context, ref string) (Container, error) {
	var inspected struct {
		ID    string `json:"Id"`
		Name  string
		State struct {
			Status string
			Health *struct{ Status string }
		}
		Config struct {
			Image  string
			Labels map[string]string
		}
	}
	if err := c.get(ctx, "/containers/"+url.PathEscape(ref)+"/json", nil, &inspected); err != nil {
		return Container{}, err
	}
	container := Container{
		ID: inspected.ID, Name: strings.TrimPrefix(inspected.Name, "/"), Image: inspected.Config.Image,
		State: inspected.State.Status, Labels: inspected.Config.Labels,
	}
	if inspected.State.Health != nil {
		container.Health = inspected.State.Health.Status
	}
	return container, nil
}

// Stats takes one sample of a running container.
func (c *Client) Stats(ctx context.Context, ref string) (Stats, error) {
	var raw struct {
		Read     time.Time `json:"read"`
		CPUStats struct {
			CPUUsage struct {
				TotalUsage uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			OnlineCPUs int `json:"online_cpus"`
		} `json:"cpu_stats"`
		MemoryStats struct {
			Usage uint64            `json:"usage"`
			Limit uint64            `json:"limit"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			RxBytes uint64 `json:"rx_bytes"`
			TxBytes uint64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	query := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	if err := c.get(ctx, "/containers/"+url.PathEscape(ref)+"/stats", query, &raw); err != nil {
		return Stats{}, err
	}
	// Docker answers a stopped container with a zero read time.
	if raw.Read.IsZero() || raw.Read.Year() < 2000 {
		return Stats{}, ErrNotRunning
	}
	stats := Stats{
		Read: raw.Read, CPUNanos: raw.CPUStats.CPUUsage.TotalUsage, OnlineCPUs: raw.CPUStats.OnlineCPUs,
		MemoryBytes: raw.MemoryStats.Usage, MemoryLimitBytes: raw.MemoryStats.Limit,
	}
	cache := raw.MemoryStats.Stats["inactive_file"]
	if cache == 0 {
		cache = raw.MemoryStats.Stats["total_inactive_file"]
	}
	if cache < stats.MemoryBytes {
		stats.MemoryBytes -= cache
	}
	for _, network := range raw.Networks {
		stats.NetOK = true
		stats.NetRx += network.RxBytes
		stats.NetTx += network.TxBytes
	}
	return stats, nil
}

func (c *Client) get(ctx context.Context, path string, query url.Values, into any) error {
	target := *c.base
	target.Path = path
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("engine %s: %w", path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusConflict, resp.StatusCode == http.StatusInternalServerError && strings.HasSuffix(path, "/stats"):
		// Podman answers stats of a stopped container with 500 "container is stopped".
		return ErrNotRunning
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("engine %s: answered %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(into)
}
