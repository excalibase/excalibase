package tableimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// Google Sheets are read only through the CSV export of a sheet shared as
// "anyone with the link" or published to the web. The link the user pastes
// is parsed for its ids and a fresh URL is built from them: the user's
// string is never fetched, so no other host is reachable (SSRF).

var (
	ErrNotASheetsURL   = errors.New("paste a Google Sheets link (https://docs.google.com/spreadsheets/d/...)")
	ErrSheetNotPublic  = errors.New("the sheet is not readable without signing in; share it as \"anyone with the link\" or publish it to the web, then try again")
	ErrSheetDownload   = errors.New("the sheet could not be downloaded from Google")
	errRedirectRefused = errors.New("the sheet answered with a redirect to a host that is not Google Sheets")
)

const (
	sheetsHost   = "docs.google.com"
	maxRedirects = 5
	fetchTimeout = 5 * time.Minute
)

var (
	sheetIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{25,100}$`)
	publishedPattern = regexp.MustCompile(`^2PACX-[A-Za-z0-9_-]{10,200}$`)
	gidPattern       = regexp.MustCompile(`^[0-9]{1,12}$`)
	// The export answers with a redirect to a per-document download host.
	downloadHostPattern = regexp.MustCompile(`^doc-[a-z0-9-]{1,40}-sheets\.googleusercontent\.com$`)
)

// SheetRef names one sheet of a spreadsheet, either by document id or by
// published id.
type SheetRef struct {
	ID        string
	Published bool
	GID       string
}

// ParseSheetsURL accepts a docs.google.com spreadsheet link and nothing else.
func ParseSheetsURL(raw string) (SheetRef, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		strings.ToLower(parsed.Hostname()) != sheetsHost || parsed.RawPath != "" {
		return SheetRef{}, ErrNotASheetsURL
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	ref, ok := refFromSegments(segments)
	if !ok {
		return SheetRef{}, ErrNotASheetsURL
	}
	gid, ok := gidFrom(parsed)
	if !ok {
		return SheetRef{}, ErrNotASheetsURL
	}
	ref.GID = gid
	return ref, nil
}

func refFromSegments(segments []string) (SheetRef, bool) {
	if len(segments) < 3 || segments[0] != "spreadsheets" || segments[1] != "d" {
		return SheetRef{}, false
	}
	if segments[2] == "e" && len(segments) >= 4 && publishedPattern.MatchString(segments[3]) {
		return SheetRef{ID: segments[3], Published: true}, true
	}
	if sheetIDPattern.MatchString(segments[2]) {
		return SheetRef{ID: segments[2]}, true
	}
	return SheetRef{}, false
}

// gidFrom reads the sheet tab from ?gid= or #gid=; the first tab otherwise.
func gidFrom(parsed *url.URL) (string, bool) {
	gid := parsed.Query().Get("gid")
	if fragment, found := strings.CutPrefix(parsed.Fragment, "gid="); found {
		gid = fragment
	}
	if gid == "" {
		return "0", true
	}
	return gid, gidPattern.MatchString(gid)
}

func (r SheetRef) exportPath() string {
	if r.Published {
		return "/spreadsheets/d/e/" + r.ID + "/pub?output=csv&gid=" + r.GID
	}
	return "/spreadsheets/d/" + r.ID + "/export?format=csv&gid=" + r.GID
}

// SheetsFetcher downloads a sheet's CSV export.
type SheetsFetcher struct {
	client *http.Client
	base   *url.URL
}

// NewSheetsFetcher reaches docs.google.com over a client that follows
// redirects only to Google's sheet hosts and connects only to public
// addresses, checked on the address actually dialled.
func NewSheetsFetcher() *SheetsFetcher {
	return &SheetsFetcher{
		client: newSheetsClient(fetchTimeout),
		base:   &url.URL{Scheme: "https", Host: sheetsHost},
	}
}

func newSheetsClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: refuseInternalAddress}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: checkRedirect}
}

func refuseInternalAddress(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !isPublicAddress(host) {
		return fmt.Errorf("refused to connect to %s: not a public address", address)
	}
	return nil
}

// Global-unicast ranges that still lead inside or nowhere public: carrier-grade
// NAT, IETF/benchmark/reserved IPv4, and the NAT64 and 6to4 prefixes that
// carry an IPv4 address, internal ones included.
var internalPrefixes = []netip.Prefix{
	v4Prefix(100, 64, 0, 0, 10),
	v4Prefix(192, 0, 0, 0, 24),
	v4Prefix(198, 18, 0, 0, 15),
	v4Prefix(240, 0, 0, 0, 4),
	v6Prefix([]byte{0x00, 0x64, 0xff, 0x9b}, 96),
	v6Prefix([]byte{0x00, 0x64, 0xff, 0x9b, 0x00, 0x01}, 48),
	v6Prefix([]byte{0x20, 0x02}, 16),
}

func v4Prefix(a, b, c, d byte, bits int) netip.Prefix {
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{a, b, c, d}), bits)
}

func v6Prefix(lead []byte, bits int) netip.Prefix {
	var addr [16]byte
	copy(addr[:], lead)
	return netip.PrefixFrom(netip.AddrFrom16(addr), bits)
}

func isPublicAddress(host string) bool {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range internalPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errRedirectRefused
	}
	host := strings.ToLower(req.URL.Hostname())
	if host == "accounts.google.com" {
		return ErrSheetNotPublic
	}
	if req.URL.Scheme != "https" || req.URL.Port() != "" || req.URL.User != nil ||
		(host != sheetsHost && !downloadHostPattern.MatchString(host)) {
		return errRedirectRefused
	}
	return nil
}

// Fetch returns the sheet's CSV body, cut off after maxBytes+1 so the CSV
// reader's cap reports an oversized sheet. The caller closes it.
func (f *SheetsFetcher) Fetch(ctx context.Context, ref SheetRef, maxBytes int64) (io.ReadCloser, error) {
	target := *f.base
	path, query, _ := strings.Cut(ref.exportPath(), "?")
	target.Path, target.RawQuery = path, query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrSheetNotPublic) {
			return nil, ErrSheetNotPublic
		}
		return nil, fmt.Errorf("%w: %v", ErrSheetDownload, unwrapURLError(err))
	}
	if resp.StatusCode != http.StatusOK || !isCSV(resp.Header.Get("Content-Type")) {
		resp.Body.Close()
		return nil, ErrSheetNotPublic
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(resp.Body, maxBytes+1), resp.Body}, nil
}

func unwrapURLError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func isCSV(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && (mediaType == "text/csv" || mediaType == "text/plain")
}
