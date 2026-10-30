package hostaddr

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fibTrie is a host's /proc/net/fib_trie: loopback, a docker bridge, a public NIC
// address and a whole public /29 routed to lo, in the Main and Local tables.
const fibTrie = `Main:
  +-- 0.0.0.0/0 3 0 4
     |-- 0.0.0.0
        /0 universe UNICAST
     +-- 127.0.0.0/8 2 0 2
        +-- 127.0.0.0/31 1 0 0
           |-- 127.0.0.0
              /8 host LOCAL
           |-- 127.0.0.1
              /32 host LOCAL
        |-- 127.255.255.255
           /32 link BROADCAST
     +-- 172.17.0.0/31 1 0 0
        |-- 172.17.0.0
           /16 link UNICAST
        |-- 172.17.0.1
           /32 host LOCAL
     |-- 51.68.10.0
        /24 link UNICAST
     |-- 51.68.10.20
        /32 host LOCAL
     |-- 203.0.113.8
        /29 host LOCAL
Local:
  +-- 0.0.0.0/0 3 0 4
     |-- 51.68.10.20
        /32 host LOCAL
`

const ifInet6 = `00000000000000000000000000000001 01 80 10 80       lo
2001067c2b8c000000000000000000aa 02 40 00 80     eth0
fe800000000000006cd09efffe47392a 17 40 20 80  docker0
`

func prefixes(t *testing.T, raw ...string) []netip.Prefix {
	t.Helper()
	out := make([]netip.Prefix, 0, len(raw))
	for _, item := range raw {
		out = append(out, netip.MustParsePrefix(item))
	}
	return out
}

func TestParseFibTrie_LocalAddressesOnce(t *testing.T) {
	got, err := ParseFibTrie(strings.NewReader(fibTrie))
	if err != nil {
		t.Fatal(err)
	}
	want := prefixes(t, "127.0.0.0/8", "127.0.0.1/32", "172.17.0.1/32", "51.68.10.20/32", "203.0.113.8/29")
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseFibTrie_RefusesATableWithoutLocalAddresses(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "no local": "Main:\n  |-- 10.0.0.0\n     /8 link UNICAST\n"} {
		if _, err := ParseFibTrie(strings.NewReader(body)); !errors.Is(err, ErrNoAddresses) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The kernel adds " tos=N" to a route with a TOS, and a local route may have
// any scope (ip route add local ... scope link): both are still the host's.
func TestParseFibTrie_LocalWithTOSOrAnotherScope(t *testing.T) {
	body := "Main:\n  |-- 192.0.2.0\n     /24 link LOCAL\n  |-- 198.51.100.4\n     /32 host LOCAL tos=8\n  |-- 198.51.100.5\n     /32 host UNICAST\n"
	got, err := ParseFibTrie(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if want := prefixes(t, "192.0.2.0/24", "198.51.100.4/32"); !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A table cut short would drop addresses: too long is an error, not a partial set.
func TestParse_RefusesAnOversizedFile(t *testing.T) {
	huge := strings.Repeat("x", maxFileBytes) + "\n  |-- 198.51.100.4\n     /32 host LOCAL\n"
	if _, err := ParseFibTrie(strings.NewReader("Main:\n  |-- 127.0.0.1\n     /32 host LOCAL\n" + huge)); err == nil {
		t.Error("an oversized routing table parsed")
	}
	if _, err := ParseIfInet6(strings.NewReader(strings.Repeat("0", maxFileBytes+1))); err == nil {
		t.Error("an oversized IPv6 table parsed")
	}
}

func TestParseFibTrie_SkipsMalformedLines(t *testing.T) {
	body := "Main:\n  |-- not-an-ip\n     /32 host LOCAL\n  |-- 198.51.100.1\n     /99 host LOCAL\n     /32 host LOCAL\n"
	got, err := ParseFibTrie(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if want := prefixes(t, "198.51.100.1/32"); !slices.Equal(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestParseIfInet6_EveryAddress(t *testing.T) {
	got, err := ParseIfInet6(strings.NewReader(ifInet6 + "zz short\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := prefixes(t, "::1/128", "2001:67c:2b8c::aa/128", "fe80::6cd0:9eff:fe47:392a/128")
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseIfInet6_EmptyIsNoIPv6(t *testing.T) {
	got, err := ParseIfInet6(strings.NewReader(""))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
}

func writeFiles(t *testing.T, trie, inet6 string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	triePath, inet6Path := filepath.Join(dir, "fib_trie"), filepath.Join(dir, "if_inet6")
	for path, body := range map[string]string{triePath: trie, inet6Path: inet6} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return triePath, inet6Path
}

func TestLoad_ReadsBothFiles(t *testing.T) {
	triePath, inet6Path := writeFiles(t, fibTrie, ifInet6)
	src, err := Load(Files{FibTrie: triePath, IfInet6: inet6Path})
	if err != nil {
		t.Fatal(err)
	}
	got := src.Prefixes()
	for _, want := range prefixes(t, "51.68.10.20/32", "2001:67c:2b8c::aa/128") {
		if !slices.Contains(got, want) {
			t.Errorf("%v missing from %v", want, got)
		}
	}
}

func TestLoad_FailsClosed(t *testing.T) {
	triePath, inet6Path := writeFiles(t, "", ifInet6)
	cases := map[string]Files{
		"no table named":   {IfInet6: inet6Path},
		"table unreadable": {FibTrie: filepath.Join(t.TempDir(), "missing")},
		"table empty":      {FibTrie: triePath},
		"inet6 unreadable": {FibTrie: writeOne(t, fibTrie), IfInet6: filepath.Join(t.TempDir(), "missing")},
	}
	for name, files := range cases {
		if _, err := Load(files); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	// No IPv6 file at all is a host without IPv6.
	if _, err := Load(Files{FibTrie: writeOne(t, fibTrie)}); err != nil {
		t.Errorf("IPv4 only: %v", err)
	}
}

func writeOne(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fib_trie")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRefresh_PicksUpANewAddressAndKeepsTheLastGoodSet(t *testing.T) {
	triePath, inet6Path := writeFiles(t, fibTrie, ifInet6)
	src, err := Load(Files{FibTrie: triePath, IfInet6: inet6Path})
	if err != nil {
		t.Fatal(err)
	}
	added := strings.Replace(fibTrie, "Local:", "     |-- 198.51.100.1\n        /32 host LOCAL\nLocal:", 1)
	if err := os.WriteFile(triePath, []byte(added), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := src.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(src.Prefixes(), netip.MustParsePrefix("198.51.100.1/32")) {
		t.Error("a new host address was not picked up")
	}
	if err := os.Remove(triePath); err != nil {
		t.Fatal(err)
	}
	if err := src.Refresh(); err == nil {
		t.Error("a vanished table refreshed")
	}
	if !slices.Contains(src.Prefixes(), netip.MustParsePrefix("51.68.10.20/32")) {
		t.Error("a failed refresh dropped the last good set")
	}
}

func TestPrefixes_IsACopy(t *testing.T) {
	src, err := Load(Files{FibTrie: writeOne(t, fibTrie)})
	if err != nil {
		t.Fatal(err)
	}
	got := src.Prefixes()
	got[0] = netip.MustParsePrefix("8.8.8.8/32")
	if slices.Contains(src.Prefixes(), netip.MustParsePrefix("8.8.8.8/32")) {
		t.Error("a caller changed the source's set")
	}
}

func TestRun_RefreshesUntilCancelled(t *testing.T) {
	triePath := writeOne(t, fibTrie)
	src, err := Load(Files{FibTrie: triePath})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { src.Run(ctx, 5*time.Millisecond); close(done) }()
	added := strings.Replace(fibTrie, "Local:", "     |-- 198.51.100.9\n        /32 host LOCAL\nLocal:", 1)
	if err := os.WriteFile(triePath, []byte(added), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !slices.Contains(src.Prefixes(), netip.MustParsePrefix("198.51.100.9/32")) {
		if time.Now().After(deadline) {
			t.Fatal("Run never refreshed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestBoundedReader_RefusesOneByteTooMany(t *testing.T) {
	read := func(size int) error {
		_, err := io.ReadAll(newBoundedReader(strings.NewReader(strings.Repeat("a", size)), 8))
		return err
	}
	if err := read(8); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	if err := read(9); !errors.Is(err, errTooLarge) {
		t.Errorf("one over: %v", err)
	}
}

// A table that is the container's own (a wrong /proc/1, an edited mount) has
// only loopback and the container's addresses: the forwarder must not start.
func TestLoad_RefusesTheContainersOwnTable(t *testing.T) {
	own := "Main:\n  |-- 127.0.0.1\n     /32 host LOCAL\n  |-- 172.30.0.5\n     /32 host LOCAL\n"
	ownPath, alsoOwn := writeOne(t, own), writeOne(t, own)
	if _, err := Load(Files{FibTrie: alsoOwn, Self: ownPath}); !errors.Is(err, ErrNotHostTable) {
		t.Errorf("the container's own table: %v", err)
	}
	if _, err := Load(Files{FibTrie: writeOne(t, fibTrie), Self: ownPath}); err != nil {
		t.Errorf("the host's table: %v", err)
	}
	if _, err := Load(Files{FibTrie: writeOne(t, fibTrie), Self: filepath.Join(t.TempDir(), "gone")}); err == nil {
		t.Error("loaded without reading the container's own table")
	}
}
