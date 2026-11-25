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

// Every XML type is plain text: a browser renders XML as XHTML or SVG from a
// namespace it can reach by a character reference, an entity, a UTF-16 body or a
// stylesheet, so the body is never trusted.
func TestRuntimeResponseServesEveryXMLTypeAsPlainText(t *testing.T) {
	bodies := map[string]string{
		"plain xml":      `<?xml version="1.0"?><urlset><url/></urlset>`,
		"char reference": `<html xmlns="http&#x3a;//www.w3.org/1999/xhtml"><body>x</body></html>`,
		"entity":         `<!DOCTYPE html [<!ENTITY ns "http://www.w3.org/1999/xhtml">]><html xmlns="&ns;"/>`,
		"utf-16 nuls":    "<\x00h\x00t\x00m\x00l\x00 \x00x\x00m\x00l\x00n\x00s\x00",
		"stylesheet":     `<?xml-stylesheet type="text/xsl" href="/functions/v1/p/sheet"?><doc/>`,
	}
	for _, contentType := range []string{
		"text/xml", "application/xml", "Application/XML; charset=utf-8", "text/xml; charset=utf-16le",
		"text/xsl", "text/rdf", "Text/RDF; charset=utf-8", "application/xslt+xml", "application/rss+xml", "application/atom+xml", "application/soap+xml",
		"application/rdf+xml", "application/vnd.foo+xml", "image/svg+xml", "application/mathml+xml",
	} {
		for name, body := range bodies {
			if got := relayBody(map[string]string{"content-type": contentType}, body, true).Header().Get("Content-Type"); got != plainText {
				t.Errorf("%q with %s served as %q", contentType, name, got)
			}
		}
	}
}

func TestRuntimeResponseServesMultipartAsPlainText(t *testing.T) {
	for _, contentType := range []string{
		"multipart/x-mixed-replace; boundary=x", "Multipart/X-Mixed-Replace;boundary=\"a,b\"", "multipart/mixed; boundary=x", "multipart/form-data",
	} {
		body := "--x\r\nContent-Type: text/html\r\n\r\n<h1>login</h1>\r\n--x--"
		if got := relayBody(map[string]string{"content-type": contentType}, body, true).Header().Get("Content-Type"); got != plainText {
			t.Errorf("%q served as %q", contentType, got)
		}
	}
}

// A type the browser cannot place is guessed from the body, which a function
// controls; it is fixed here the way a missing type is.
func TestRuntimeResponseTreatsAnUnplaceableTypeLikeAMissingOne(t *testing.T) {
	for _, contentType := range []string{"*/*", "unknown/unknown", "application/unknown", "garbage", "text/", "/html", "text/html/x y", ";", "text/ht ml", "text/*", "*/html", "*/xml"} {
		if got := relayBody(map[string]string{"content-type": contentType}, "<html><body>login</body></html>", true).Header().Get("Content-Type"); got != plainText {
			t.Errorf("%q with an HTML body served as %q", contentType, got)
		}
		if got := relayBody(map[string]string{"content-type": contentType}, "<?xml version='1.0'?><doc/>", true).Header().Get("Content-Type"); got != plainText {
			t.Errorf("%q with an XML body served as %q", contentType, got)
		}
		if got := relayBody(map[string]string{"content-type": contentType}, "hello", true).Header().Get("Content-Type"); got != plainText {
			t.Errorf("%q with a text body served as %q", contentType, got)
		}
	}
}

func TestRuntimeResponseWithoutATypeAndAnXMLBodyIsPlainText(t *testing.T) {
	if got := relayBody(nil, `<?xml version="1.0"?><html xmlns="http://www.w3.org/1999/xhtml"/>`, true).Header().Get("Content-Type"); got != plainText {
		t.Errorf("served as %q", got)
	}
}

// A quoted parameter can hold a comma; only a comma outside quotes separates two types.
func TestRuntimeResponseSplitsTypesOnlyOutsideQuotes(t *testing.T) {
	for contentType, want := range map[string]string{
		`application/json; note="a,b"`:          `application/json; note="a,b"`,
		`application/json; note="a, text/html"`: `application/json; note="a, text/html"`,
		`application/json; note="x", text/html`: plainText,
		`text/plain; a="\"", text/html`:         plainText,
	} {
		if got := relayBody(map[string]string{"content-type": contentType}, "{}", true).Header().Get("Content-Type"); got != want {
			t.Errorf("%q served as %q, want %q", contentType, got, want)
		}
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

// text/html;; is a malformed parameter list, not a second type: next to a JSON
// type it stays JSON, while a separate text/html value still wins.
func TestRuntimeResponseMalformedHTMLParametersNextToJSON(t *testing.T) {
	got := relayBody(map[string]string{"content-type": "application/json; text/html;;"}, "{}", true).Header().Get("Content-Type")
	if mediaTypeOfValue(got) != "application/json" {
		t.Errorf("one value served as %q, want application/json", got)
	}
	if got := relayBody(map[string]string{"content-type": "application/json, text/html;;"}, "{}", true).Header().Get("Content-Type"); got != plainText {
		t.Errorf("two values served as %q, want plain text", got)
	}
}

func mediaTypeOfValue(value string) string {
	mediaType, _ := parseMediaType(value)
	return mediaType
}
