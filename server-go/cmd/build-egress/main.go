// Command build-egress is the forward proxy a git build on a single Docker or
// Podman host reaches the internet through (EXC-671). provisioning starts one
// per build, beside the build's network, from its own image.
//
//	EGRESS_LISTEN            address to listen on (default :3128)
//	EGRESS_EXTRA_DENY_CIDRS  ranges denied beyond the private list (comma-separated)
//	EGRESS_ALLOW             host:port entries admitted past the address check (comma-separated)
//	EGRESS_LIFETIME          exit after this long (Go duration), the build's time limit
package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/buildegress"
)

type settings struct {
	listen    string
	extraDeny []string
	allow     []string
	lifetime  time.Duration
}

func loadSettings(getenv func(string) string) settings {
	listen := strings.TrimSpace(getenv("EGRESS_LISTEN"))
	if listen == "" {
		listen = ":3128"
	}
	lifetime, err := time.ParseDuration(strings.TrimSpace(getenv("EGRESS_LIFETIME")))
	if err != nil || lifetime < 0 {
		lifetime = 0
	}
	return settings{listen: listen, extraDeny: list(getenv("EGRESS_EXTRA_DENY_CIDRS")), allow: list(getenv("EGRESS_ALLOW")), lifetime: lifetime}
}

func list(raw string) []string {
	var items []string
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func newServer(egress settings) (*http.Server, error) {
	policy, err := buildegress.NewPolicy(egress.extraDeny, egress.allow)
	if err != nil {
		return nil, err
	}
	proxy := &buildegress.Proxy{Policy: policy, Resolver: net.DefaultResolver, Log: log.Default()}
	return &http.Server{Addr: egress.listen, Handler: proxy, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}, nil
}

func main() {
	log.SetFlags(0)
	egress := loadSettings(os.Getenv)
	server, err := newServer(egress)
	if err != nil {
		log.Fatalf("build egress: %v", err)
	}
	if egress.lifetime > 0 {
		time.AfterFunc(egress.lifetime, func() {
			log.Printf("build egress: the build's time limit has passed")
			os.Exit(0)
		})
	}
	log.Printf("build egress listening on %s: ports 80 and 443 to public IPv4 only", egress.listen)
	log.Fatal(server.ListenAndServe())
}
