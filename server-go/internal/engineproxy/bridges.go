package engineproxy

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// The single host's firewall (EXC-683) matches the platform's bridges by the
// interface each has on the host. Only the engine knows it, and only the
// proxy holds the engine socket, so the proxy writes it down for the firewall:
// one line per network, "<network> <interface> <subnet,...>", or
// "<network> absent" when compose made no such network (its profile is off).

const bridgeNameOption = "com.docker.network.bridge.name"

// interfaceName is a Linux interface name: at most 15 characters, none a firewall rule could read as more.
var interfaceName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

type bridgeNetwork struct {
	ID      string `json:"Id"`
	Driver  string
	Options map[string]string
	IPAM    struct{ Config []struct{ Subnet string } }
}

// ResolveBridges answers a line for each network it can name. One it cannot
// gets none, and is in the error.
func (h *Handler) ResolveBridges(ctx context.Context, names []string) ([]string, error) {
	var lines []string
	var problems []error
	for _, name := range names {
		line, err := h.resolveBridge(ctx, name)
		if err != nil {
			problems = append(problems, fmt.Errorf("network %s: %w", name, err))
			continue
		}
		lines = append(lines, line)
	}
	return lines, errors.Join(problems...)
}

func (h *Handler) resolveBridge(ctx context.Context, name string) (string, error) {
	var network bridgeNetwork
	err := h.inspect(ctx, "/networks/"+url.PathEscape(name), &network)
	if errors.Is(err, errNoSuchThing) {
		return name + " absent", nil
	}
	if err != nil {
		return "", err
	}
	if network.Driver != "bridge" {
		return "", fmt.Errorf("driver %q is not a bridge", network.Driver)
	}
	iface, err := bridgeInterface(network)
	if err != nil {
		return "", err
	}
	subnets := make([]string, 0, len(network.IPAM.Config))
	for _, config := range network.IPAM.Config {
		prefix, err := netip.ParsePrefix(config.Subnet)
		if err != nil {
			return "", fmt.Errorf("subnet %q: %w", config.Subnet, err)
		}
		subnets = append(subnets, prefix.Masked().String())
	}
	if len(subnets) == 0 {
		return "", errors.New("no subnet")
	}
	return name + " " + iface + " " + strings.Join(subnets, ","), nil
}

// bridgeInterface is the name the bridge option sets, else Docker's br-<id[:12]>.
func bridgeInterface(network bridgeNetwork) (string, error) {
	iface := network.Options[bridgeNameOption]
	if iface == "" {
		if len(network.ID) < 12 {
			return "", fmt.Errorf("id %q is too short to name a bridge", network.ID)
		}
		iface = "br-" + network.ID[:12]
	}
	if !interfaceName.MatchString(iface) {
		return "", fmt.Errorf("bridge %q is not an interface name", iface)
	}
	return iface, nil
}

// WriteBridges replaces the file whole, so the firewall never reads half of it.
func WriteBridges(path string, lines []string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bridges-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	body := strings.Join(lines, "\n")
	if body != "" {
		body += "\n"
	}
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
