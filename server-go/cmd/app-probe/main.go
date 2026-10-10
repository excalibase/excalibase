// Command app-probe is the readiness check provisioning runs inside an app's
// container on a single Docker or Podman host (EXC-575), as the kubelet's
// probe does on Kubernetes: it reaches the container's own address.
//
//	app-probe http <port> <path>   passes on a 2xx or 3xx answer
//	app-probe tcp <port>           passes when the port accepts a connection
//
// Exit 0 is ready; anything else is not.
package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const probeTimeout = 3 * time.Second

func main() {
	if err := probe(os.Args[1:], ownAddress(), probeTimeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func probe(args []string, host string, timeout time.Duration) error {
	if len(args) < 2 {
		return errors.New("usage: app-probe http <port> <path> | tcp <port>")
	}
	port, err := strconv.Atoi(args[1])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("port %q is not a port", args[1])
	}
	address := net.JoinHostPort(host, strconv.Itoa(port))
	switch {
	case args[0] == "tcp" && len(args) == 2:
		conn, err := net.DialTimeout("tcp", address, timeout)
		if err != nil {
			return err
		}
		return conn.Close()
	case args[0] == "http" && len(args) == 3 && strings.HasPrefix(args[2], "/"):
		return probeHTTP("http://"+address+args[2], timeout) //NOSONAR the app's own port inside its container, as the kubelet probes it
	}
	return fmt.Errorf("unknown probe %q", strings.Join(args, " "))
}

// probeHTTP follows no redirect: a redirect is an answer, as the kubelet counts it.
func probeHTTP(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("%s answered %d", url, resp.StatusCode)
	}
	return nil
}

// ownAddress is the container's address on its network, where the edge
// reaches it; loopback only when it has none.
func ownAddress() string {
	addresses, err := net.InterfaceAddrs()
	if err == nil {
		for _, address := range addresses {
			if ipNet, ok := address.(*net.IPNet); ok && !ipNet.IP.IsLoopback() && ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
