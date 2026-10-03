package tableimport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const sheetID = "1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"

func TestParseSheetsURL_AcceptsGoogleSheetLinks(t *testing.T) {
	cases := map[string]string{
		"https://docs.google.com/spreadsheets/d/" + sheetID + "/edit#gid=123":                "/spreadsheets/d/" + sheetID + "/export?format=csv&gid=123",
		"https://docs.google.com/spreadsheets/d/" + sheetID + "/edit?usp=sharing":            "/spreadsheets/d/" + sheetID + "/export?format=csv&gid=0",
		"https://docs.google.com/spreadsheets/d/" + sheetID + "/export?format=csv&gid=7":     "/spreadsheets/d/" + sheetID + "/export?format=csv&gid=7",
		"https://docs.google.com/spreadsheets/d/e/2PACX-1vQabc_DEF-123/pub?output=csv&gid=5": "/spreadsheets/d/e/2PACX-1vQabc_DEF-123/pub?output=csv&gid=5",
		"  https://docs.google.com/spreadsheets/d/" + sheetID + "  ":                         "/spreadsheets/d/" + sheetID + "/export?format=csv&gid=0",
	}
	for input, wantPath := range cases {
		ref, err := ParseSheetsURL(input)
		if err != nil {
			t.Errorf("%q refused: %v", input, err)
			continue
		}
		if got := ref.exportPath(); got != wantPath {
			t.Errorf("%q -> %q, want %q", input, got, wantPath)
		}
	}
}

// Only a Google Sheets link is ever fetched; everything else is refused
// before any request is made (SSRF).
func TestParseSheetsURL_RefusesEverythingElse(t *testing.T) {
	for _, bad := range []string{
		"",
		"http://docs.google.com/spreadsheets/d/" + sheetID,
		"https://docs.google.com.evil.example/spreadsheets/d/" + sheetID,
		"https://evil.example/docs.google.com/spreadsheets/d/" + sheetID,
		"https://docs.google.com@evil.example/spreadsheets/d/" + sheetID,
		"https://user:pw@docs.google.com/spreadsheets/d/" + sheetID,
		"https://docs.google.com:8443/spreadsheets/d/" + sheetID,
		"https://drive.google.com/file/d/" + sheetID,
		"https://docs.google.com/document/d/" + sheetID,
		"https://docs.google.com/spreadsheets/d/abc",
		"https://docs.google.com/spreadsheets/d/" + sheetID + "%2F..%2F/edit",
		"https://docs.google.com/spreadsheets/d/" + sheetID + "/edit#gid=abc",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"gopher://docs.google.com/spreadsheets/d/" + sheetID,
		"https://127.0.0.1/spreadsheets/d/" + sheetID,
		"https://DOCS.GOOGLE.COM./spreadsheets/d/" + sheetID,
	} {
		if _, err := ParseSheetsURL(bad); !errors.Is(err, ErrNotASheetsURL) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestRedirectPolicy_OnlyGoogleSheetsHosts(t *testing.T) {
	allowed := []string{
		"https://docs.google.com/spreadsheets/d/x/export",
		"https://doc-08-4o-sheets.googleusercontent.com/export/abc",
	}
	for _, raw := range allowed {
		if err := checkRedirect(mustRequest(t, raw), nil); err != nil {
			t.Errorf("%s refused: %v", raw, err)
		}
	}
	refused := []string{
		"http://doc-08-4o-sheets.googleusercontent.com/export/abc",
		"https://accounts.google.com/ServiceLogin",
		"https://evil.example/",
		"https://sheets.googleusercontent.com.evil.example/",
		"https://lh3.googleusercontent.com/x",
		"https://169.254.169.254/",
		"https://doc-08-4o-sheets.googleusercontent.com:444/export",
	}
	for _, raw := range refused {
		if err := checkRedirect(mustRequest(t, raw), nil); err == nil {
			t.Errorf("%s allowed", raw)
		}
	}
	via := make([]*http.Request, maxRedirects)
	if err := checkRedirect(mustRequest(t, allowed[1]), via); err == nil {
		t.Error("an endless redirect chain was followed")
	}
}

// A private sheet redirects to Google's sign-in page, which is not an
// allowed host: the user is told to publish or share the sheet.
func TestRedirectPolicy_SignInPageMeansNotPublic(t *testing.T) {
	err := checkRedirect(mustRequest(t, "https://accounts.google.com/ServiceLogin?continue=x"), nil)
	if !errors.Is(err, ErrSheetNotPublic) {
		t.Fatalf("err = %v", err)
	}
}

func TestPublicAddress_RefusesInternalAddresses(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.64.0.1", "0.0.0.0", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "224.0.0.1", "64:ff9b::a00:1", "64:ff9b:1::a00:1", "2002:a00:1::1", "198.18.0.1", "240.0.0.1", "192.0.0.8"} {
		if isPublicAddress(ip) {
			t.Errorf("%s counted as public", ip)
		}
	}
	for _, ip := range []string{"142.250.72.14", "2607:f8b0:4005:80b::200e"} {
		if !isPublicAddress(ip) {
			t.Errorf("%s counted as internal", ip)
		}
	}
}

func TestFetcher_GuardedDialRefusesLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a\n1\n"))
	}))
	defer server.Close()
	client := newSheetsClient(5 * time.Second)
	_, err := client.Get(server.URL)
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("err = %v", err)
	}
}

func sheetsTestFetcher(t *testing.T, handler http.HandlerFunc) (*SheetsFetcher, SheetRef) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	fetcher := &SheetsFetcher{client: server.Client(), base: base}
	ref, err := ParseSheetsURL("https://docs.google.com/spreadsheets/d/" + sheetID + "/edit#gid=3")
	if err != nil {
		t.Fatal(err)
	}
	return fetcher, ref
}

func TestFetcher_ReturnsTheCSVBodyUpToTheCap(t *testing.T) {
	var gotPath string
	fetcher, ref := sheetsTestFetcher(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte("a,b\n1,2\n"))
	})
	body, err := fetcher.Fetch(context.Background(), ref, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	src, _ := NewCSVSource(body, ',', testLimits())
	rows, err := readAll(t, src)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%q err=%v", rows, err)
	}
	if gotPath != "/spreadsheets/d/"+sheetID+"/export?format=csv&gid=3" {
		t.Fatalf("fetched %s", gotPath)
	}
}

func TestFetcher_AnHTMLAnswerMeansTheSheetIsNotPublic(t *testing.T) {
	fetcher, ref := sheetsTestFetcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>sign in</html>"))
	})
	if _, err := fetcher.Fetch(context.Background(), ref, 1<<20); !errors.Is(err, ErrSheetNotPublic) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetcher_ErrorStatusIsReported(t *testing.T) {
	fetcher, ref := sheetsTestFetcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	if _, err := fetcher.Fetch(context.Background(), ref, 1<<20); !errors.Is(err, ErrSheetNotPublic) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetcher_AnOversizedSheetStopsAtTheCap(t *testing.T) {
	fetcher, ref := sheetsTestFetcher(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		for i := 0; i < 1000; i++ {
			_, _ = w.Write([]byte("0123456789\n"))
		}
	})
	body, err := fetcher.Fetch(context.Background(), ref, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	lim := testLimits()
	lim.MaxBytes = 100
	src, _ := NewCSVSource(body, ',', lim)
	if _, err := readAll(t, src); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("err = %v", err)
	}
}

func mustRequest(t *testing.T, raw string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}
