package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/edgefn"
)

// EXC-810: the shared API origin never serves a function's HTML, XHTML or SVG
// as a page; a fake login form there would sit on the platform's own host.

const plainText = "text/plain; charset=utf-8"

func relayBody(headers map[string]string, body string, guard bool) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	writeRuntimeResponse(rec, testFnProject, &edgefn.InvokeResponse{Status: 200, Headers: headers, Body: body}, guard)
	return rec
}

func TestRuntimeResponseServesRenderableTypesAsPlainText(t *testing.T) {
	for _, contentType := range []string{
		"text/html", "text/html; charset=utf-8", "Text/HTML; charset=utf-8", "TEXT/HTML", " text/html ",
		`text/html;charset="UTF-8"`, "text/html;", "text/html ; q=1",
		"application/xhtml+xml", "Application/XHTML+XML; charset=utf-8",
		"image/svg+xml", "IMAGE/SVG+XML", "image/svg+xml; charset=utf-8",
	} {
		rec := relayBody(map[string]string{"content-type": contentType}, "<h1>log in</h1>", true)
		if got := rec.Header().Get("Content-Type"); got != plainText {
			t.Errorf("%q served as %q, want %q", contentType, got, plainText)
		}
		if rec.Body.String() != "<h1>log in</h1>" {
			t.Errorf("%q: body changed to %q", contentType, rec.Body.String())
		}
	}
}

func TestRuntimeResponseLeavesDataTypesAlone(t *testing.T) {
	for _, contentType := range []string{
		"application/json", "application/json; charset=utf-8", "text/plain", "text/css", "text/csv",
		"application/javascript", "image/png", "application/octet-stream", "text/html-sandboxed", "application/xhtml",
		"image/svg", "text/htmlx",
	} {
		rec := relayBody(map[string]string{"content-type": contentType}, "{}", true)
		if got := rec.Header().Get("Content-Type"); got != contentType {
			t.Errorf("%q changed to %q", contentType, got)
		}
	}
}

func TestRuntimeResponseHeaderNameCaseDoesNotMatter(t *testing.T) {
	for _, name := range []string{"Content-Type", "CONTENT-TYPE", "content-type"} {
		if got := relayBody(map[string]string{name: "text/html"}, "x", true).Header().Get("Content-Type"); got != plainText {
			t.Errorf("header %q: served as %q", name, got)
		}
	}
}

// The runtime joins repeated headers into one comma-separated value, and a
// differently-cased duplicate key can sit beside it: any renderable one counts.
func TestRuntimeResponseSeesEveryContentTypeTheFunctionSent(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"joined, html last":  {"content-type": "text/plain, text/html"},
		"joined, html first": {"content-type": "text/html, application/json"},
		"joined, svg middle": {"content-type": "text/plain, image/svg+xml; charset=utf-8, text/css"},
		"two keys":           {"content-type": "text/plain", "Content-Type": "text/html"},
		"two keys reversed":  {"content-type": "text/html", "Content-Type": "text/plain"},
		"three keys":         {"Content-type": "application/json", "content-type": "text/plain", "CONTENT-TYPE": "application/xhtml+xml"},
	} {
		for i := 0; i < 20; i++ { // map order is random; the answer must not depend on it
			rec := relayBody(headers, "<p>x</p>", true)
			if got := rec.Header().Values("Content-Type"); len(got) != 1 || got[0] != plainText {
				t.Fatalf("%s: Content-Type = %v, want one %q", name, got, plainText)
			}
		}
	}
}

func TestRuntimeResponseWithoutAContentTypeIsNotSniffedIntoAPage(t *testing.T) {
	for name, body := range map[string]string{
		"html doctype": "<!DOCTYPE html><html><body><form>", "html tag": "<html><head></head>", "padded": "  \n\t<html>",
		"h1": "<h1>log in</h1>", "script": "<script>alert(1)</script>", "comment": "<!-- x --><p>hi",
		"upper": "<HTML>", "div": "<div>fake</div>",
	} {
		rec := relayBody(map[string]string{"x-other": "1"}, body, true)
		if got := rec.Header().Get("Content-Type"); got != plainText {
			t.Errorf("%s: Content-Type = %q, want %q", name, got, plainText)
		}
		if got := rec.Result().Header.Get("Content-Type"); strings.Contains(got, "html") {
			t.Errorf("%s: sniffed as %q", name, got)
		}
	}
}

func TestRuntimeResponseWithoutAContentTypeKeepsPlainDataPlain(t *testing.T) {
	rec := relayBody(nil, `{"ok":true}`, true)
	if got := rec.Header().Get("Content-Type"); got != plainText {
		t.Errorf("Content-Type = %q, want %q", got, plainText)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestRuntimeResponseEmptyContentTypeCountsAsMissing(t *testing.T) {
	if got := relayBody(map[string]string{"content-type": ""}, "<html>", true).Header().Get("Content-Type"); got != plainText {
		t.Errorf("Content-Type = %q", got)
	}
}

// XML served as text/xml or application/xml renders as XHTML or SVG when its
// root element is in that namespace, so those are held to the same rule.
func TestRuntimeResponseXMLInAPageNamespaceIsServedAsPlainText(t *testing.T) {
	xhtml := `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"><body>x</body></html>`
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>1</script></svg>`
	for _, contentType := range []string{"text/xml", "application/xml", "Application/XML; charset=utf-8", "application/rss+xml", "application/atom+xml"} {
		for _, body := range []string{xhtml, svg} {
			if got := relayBody(map[string]string{"content-type": contentType}, body, true).Header().Get("Content-Type"); got != plainText {
				t.Errorf("%q with a page namespace served as %q", contentType, got)
			}
		}
	}
	if got := relayBody(nil, xhtml, true).Header().Get("Content-Type"); got != plainText {
		t.Errorf("xhtml without a type served as %q", got)
	}
}

func TestRuntimeResponsePlainXMLKeepsItsType(t *testing.T) {
	rec := relayBody(map[string]string{"content-type": "application/xml"}, `<?xml version="1.0"?><urlset><url/></urlset>`, true)
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Errorf("a sitemap changed to %q", got)
	}
}

func TestRuntimeResponseAlwaysSetsNosniff(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"html": {"content-type": "text/html"}, "json": {"content-type": "application/json"}, "none": nil,
		"function tries to clear it": {"x-content-type-options": ""}, "function sets something else": {"x-content-type-options": "sniff"},
	} {
		for _, guard := range []bool{true, false} {
			if got := relayBody(headers, "{}", guard).Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("%s (guard %v): X-Content-Type-Options = %q", name, guard, got)
			}
		}
	}
}

func TestRuntimeResponseHTMLRulesApplyToEveryStatus(t *testing.T) {
	for _, status := range []int{200, 201, 301, 302, 303, 307, 308, 400, 401, 404, 500} {
		rec := httptest.NewRecorder()
		writeRuntimeResponse(rec, testFnProject, &edgefn.InvokeResponse{
			Status:  status,
			Headers: map[string]string{"content-type": "text/html", "location": "https://example.test/login"},
			Body:    "<html>",
		}, true)
		if rec.Code != status || rec.Header().Get("Content-Type") != plainText {
			t.Errorf("status %d: code %d, Content-Type %q", status, rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("Location") != "https://example.test/login" {
			t.Errorf("status %d: Location was changed", status)
		}
	}
}

func TestRuntimeResponseIsTheSameForHEADAndGET(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRuntimeResponse(w, testFnProject, &edgefn.InvokeResponse{
			Status: 200, Headers: map[string]string{"content-type": "text/html; charset=utf-8"}, Body: "<html><body>hi</body></html>",
		}, true)
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(method, srv.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); got != plainText {
			t.Errorf("%s: Content-Type = %q", method, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff = %q", method, got)
		}
	}
}

// A real server sniffs a missing type from the body on GET and, for HEAD, from nothing:
// HEAD must carry the same explicit type GET does.
func TestRuntimeResponseMissingTypeIsExplicitOnHEADToo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRuntimeResponse(w, testFnProject, &edgefn.InvokeResponse{Status: 200, Body: "<html><body>hi</body></html>"}, true)
	}))
	defer srv.Close()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequest(method, srv.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if got := resp.Header.Get("Content-Type"); got != plainText {
			t.Errorf("%s: Content-Type = %q", method, got)
		}
	}
}

func TestRuntimeResponseGuardOffKeepsTheFunctionsType(t *testing.T) {
	rec := relayBody(map[string]string{"content-type": "text/html"}, "<html>", false)
	if got := rec.Header().Get("Content-Type"); got != "text/html" {
		t.Errorf("guard off: Content-Type = %q, want the function's own", got)
	}
}

func TestHTMLGuardFailsClosedWithoutFlags(t *testing.T) {
	h := &FunctionHandler{}
	if !h.htmlGuardOn(t.Context()) {
		t.Error("a handler built without flags must keep the guard on")
	}
}
