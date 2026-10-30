// Package hostaddr reads a host's own addresses from its /proc/net files, so
// a container that dials on a tenant's behalf can refuse the host it runs on
// (EXC-658): its public address, its bridge gateways, anything routed to it.
package hostaddr

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNoAddresses is a routing table without a single local address, which no
// running host has: the file is not the host's table.
var ErrNoAddresses = errors.New("no local addresses in the host's routing table")

// ErrNotHostTable is a mounted table with no local address beyond loopback
// and the container's own: it is not the host's.
var ErrNotHostTable = errors.New("the mounted routing table is not the host's")

// maxFileBytes bounds one read; a busy host's fib_trie is well under it.
const maxFileBytes = 16 << 20

// ParseFibTrie returns every prefix /proc/net/fib_trie marks LOCAL, whatever
// its scope or TOS: the addresses the kernel delivers to the host itself.
func ParseFibTrie(r io.Reader) ([]netip.Prefix, error) {
	scanner := bufio.NewScanner(newBoundedReader(r, maxFileBytes))
	var leaf netip.Addr
	var found []netip.Prefix
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		switch {
		case len(fields) == 2 && fields[0] == "|--":
			leaf, _ = netip.ParseAddr(fields[1])
		case len(fields) >= 3 && fields[2] == "LOCAL" && leaf.Is4():
			if prefix, ok := localPrefix(leaf, fields[0]); ok {
				found = appendNew(found, prefix)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the routing table: %w", err)
	}
	if len(found) == 0 {
		return nil, ErrNoAddresses
	}
	return found, nil
}

func localPrefix(leaf netip.Addr, length string) (netip.Prefix, bool) {
	bits, err := strconv.Atoi(strings.TrimPrefix(length, "/"))
	if err != nil || !strings.HasPrefix(length, "/") {
		return netip.Prefix{}, false
	}
	prefix, err := leaf.Prefix(bits)
	if err != nil {
		return netip.Prefix{}, false
	}
	return prefix, true
}

// ParseIfInet6 returns every address in /proc/net/if_inet6. An empty file is
// a host without IPv6. IPv6 local routes that are no interface's address
// (AnyIP) are not listed there.
func ParseIfInet6(r io.Reader) ([]netip.Prefix, error) {
	scanner := bufio.NewScanner(newBoundedReader(r, maxFileBytes))
	var found []netip.Prefix
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || len(fields[0]) != 32 {
			continue
		}
		if addr, ok := parseHex16(fields[0]); ok {
			found = appendNew(found, netip.PrefixFrom(addr, 128))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the IPv6 addresses: %w", err)
	}
	return found, nil
}

func parseHex16(hex string) (netip.Addr, bool) {
	var raw [16]byte
	for i := range raw {
		value, err := strconv.ParseUint(hex[2*i:2*i+2], 16, 8)
		if err != nil {
			return netip.Addr{}, false
		}
		raw[i] = byte(value)
	}
	return netip.AddrFrom16(raw), true
}

// errTooLarge ends a read rather than parse a table cut short.
var errTooLarge = errors.New("the host's table is larger than expected")

// boundedReader reads at most limit bytes and fails on the byte after them.
type boundedReader struct {
	r     io.Reader
	read  int64
	limit int64
}

func newBoundedReader(r io.Reader, limit int64) *boundedReader {
	return &boundedReader{r: io.LimitReader(r, limit+1), limit: limit}
}

func (b *boundedReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return n, errTooLarge
	}
	return n, err
}

func appendNew(list []netip.Prefix, prefix netip.Prefix) []netip.Prefix {
	for _, seen := range list {
		if seen == prefix {
			return list
		}
	}
	return append(list, prefix)
}

// Files names the host's tables as mounted into the container. FibTrie is
// required; IfInet6 empty means the host has no IPv6. Self, the container's
// own table, proves at load that FibTrie is not that one.
type Files struct {
	FibTrie string
	IfInet6 string
	Self    string
}

// Source holds the host's addresses as last read.
type Source struct {
	files   Files
	mu      sync.RWMutex
	current []netip.Prefix
}

// Load reads the host's addresses once and refuses to go on without them.
func Load(files Files) (*Source, error) {
	if files.FibTrie == "" {
		return nil, errors.New("the host's routing table is not mounted")
	}
	src := &Source{files: files}
	if err := src.Refresh(); err != nil {
		return nil, err
	}
	if files.Self != "" {
		if err := src.verifyHostSide(files.Self); err != nil {
			return nil, err
		}
	}
	return src, nil
}

// verifyHostSide needs one non-loopback local IPv4 the container lacks.
func (s *Source) verifyHostSide(selfPath string) error {
	own, err := readWith(selfPath, ParseFibTrie)
	if err != nil {
		return fmt.Errorf("the container's own table: %w", err)
	}
	for _, prefix := range s.Prefixes() {
		if prefix.Addr().Is4() && !prefix.Addr().IsLoopback() && !slices.Contains(own, prefix) {
			return nil
		}
	}
	return ErrNotHostTable
}

// Refresh reads the tables again. On failure it keeps the last good set: an
// address the host had a moment ago is still refused.
func (s *Source) Refresh() error {
	v4, err := readWith(s.files.FibTrie, ParseFibTrie)
	if err != nil {
		return err
	}
	var v6 []netip.Prefix
	if s.files.IfInet6 != "" {
		if v6, err = readWith(s.files.IfInet6, ParseIfInet6); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.current = append(v4, v6...)
	s.mu.Unlock()
	return nil
}

func readWith(path string, parse func(io.Reader) ([]netip.Prefix, error)) ([]netip.Prefix, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	return parse(file)
}

// Prefixes is the host's addresses; the caller may keep or change the copy.
func (s *Source) Prefixes() []netip.Prefix {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]netip.Prefix(nil), s.current...)
}

// Run refreshes every interval until ctx ends.
func (s *Source) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Refresh(); err != nil {
				log.Printf("host addresses: %v; keeping the last good set", err)
			}
		}
	}
}
