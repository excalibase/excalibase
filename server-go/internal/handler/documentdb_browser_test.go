package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/go-chi/chi/v5"
)

type fakeDocumentBrowser struct {
	err       error
	project   string
	ns        docbrowser.Namespace
	database  string
	find      docbrowser.FindRequest
	filter    string
	id        string
	body      string
	index     string
	lastCall  string
	documents []json.RawMessage
}

func (f *fakeDocumentBrowser) note(call, project string) error {
	f.lastCall, f.project = call, project
	return f.err
}

func (f *fakeDocumentBrowser) ListDatabases(_ context.Context, project string) ([]string, error) {
	return []string{"shop"}, f.note("ListDatabases", project)
}

func (f *fakeDocumentBrowser) ListCollections(_ context.Context, project, database string) ([]docbrowser.Collection, error) {
	f.database = database
	return []docbrowser.Collection{{Name: "orders", Type: "collection"}}, f.note("ListCollections", project)
}

func (f *fakeDocumentBrowser) CreateCollection(_ context.Context, project string, ns docbrowser.Namespace) error {
	f.ns = ns
	return f.note("CreateCollection", project)
}

func (f *fakeDocumentBrowser) DropCollection(_ context.Context, project string, ns docbrowser.Namespace) error {
	f.ns = ns
	return f.note("DropCollection", project)
}

func (f *fakeDocumentBrowser) Find(_ context.Context, project string, ns docbrowser.Namespace, req docbrowser.FindRequest) (docbrowser.Page, error) {
	f.ns, f.find = ns, req
	return docbrowser.Page{Documents: f.documents, Limit: 20}, f.note("Find", project)
}

func (f *fakeDocumentBrowser) Count(_ context.Context, project string, ns docbrowser.Namespace, filter string) (int64, error) {
	f.ns, f.filter = ns, filter
	return 7, f.note("Count", project)
}

func (f *fakeDocumentBrowser) Insert(_ context.Context, project string, ns docbrowser.Namespace, body []byte) (json.RawMessage, error) {
	f.ns, f.body = ns, string(body)
	return json.RawMessage(`{"$oid":"abc"}`), f.note("Insert", project)
}

func (f *fakeDocumentBrowser) Replace(_ context.Context, project string, ns docbrowser.Namespace, id string, body []byte) error {
	f.ns, f.id, f.body = ns, id, string(body)
	return f.note("Replace", project)
}

func (f *fakeDocumentBrowser) Update(_ context.Context, project string, ns docbrowser.Namespace, id string, body []byte) error {
	f.ns, f.id, f.body = ns, id, string(body)
	return f.note("Update", project)
}

func (f *fakeDocumentBrowser) Delete(_ context.Context, project string, ns docbrowser.Namespace, id string) error {
	f.ns, f.id = ns, id
	return f.note("Delete", project)
}

func (f *fakeDocumentBrowser) ListIndexes(_ context.Context, project string, ns docbrowser.Namespace) ([]json.RawMessage, error) {
	f.ns = ns
	return []json.RawMessage{json.RawMessage(`{"name":"_id_"}`)}, f.note("ListIndexes", project)
}

func (f *fakeDocumentBrowser) CreateIndex(_ context.Context, project string, ns docbrowser.Namespace, body []byte) (string, error) {
	f.ns, f.body = ns, string(body)
	return "total_1", f.note("CreateIndex", project)
}

func (f *fakeDocumentBrowser) DropIndex(_ context.Context, project string, ns docbrowser.Namespace, name string) error {
	f.ns, f.index = ns, name
	return f.note("DropIndex", project)
}

func (f *fakeDocumentBrowser) Sample(_ context.Context, project string, ns docbrowser.Namespace) ([]json.RawMessage, error) {
	f.ns = ns
	return []json.RawMessage{json.RawMessage(`{"a":1}`)}, f.note("Sample", project)
}

const browserBase = "/api/projects/proj-doc1/documentdb"

func browserRouter(api DocumentBrowserAPI) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/documentdb", NewDocumentBrowserHandler(api).Routes)
	return r
}

func serveBrowser(t *testing.T, api DocumentBrowserAPI, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	rec := httptest.NewRecorder()
	browserRouter(api).ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func TestDocumentBrowserRoutesReachTheService(t *testing.T) {
	coll := browserBase + "/databases/shop/collections/orders"
	cases := []struct {
		method, path, body, call string
		status                   int
	}{
		{http.MethodGet, browserBase + "/databases", "", "ListDatabases", 200},
		{http.MethodGet, browserBase + "/databases/shop/collections", "", "ListCollections", 200},
		{http.MethodPost, browserBase + "/databases/shop/collections", `{"name":"orders"}`, "CreateCollection", 201},
		{http.MethodDelete, coll, "", "DropCollection", 204},
		{http.MethodGet, coll + "/documents?filter=%7B%7D&limit=5&skip=10", "", "Find", 200},
		{http.MethodGet, coll + "/count?filter=%7B%22a%22%3A1%7D", "", "Count", 200},
		{http.MethodPost, coll + "/documents", `{"a":1}`, "Insert", 201},
		{http.MethodPut, coll + "/documents?id=%221%22", `{"a":2}`, "Replace", 204},
		{http.MethodPatch, coll + "/documents?id=%221%22", `{"$set":{"a":3}}`, "Update", 204},
		{http.MethodDelete, coll + "/documents?id=%221%22", "", "Delete", 204},
		{http.MethodGet, coll + "/indexes", "", "ListIndexes", 200},
		{http.MethodPost, coll + "/indexes", `{"keys":{"a":1}}`, "CreateIndex", 201},
		{http.MethodDelete, coll + "/indexes/a_1", "", "DropIndex", 204},
		{http.MethodGet, coll + "/sample", "", "Sample", 200},
	}
	for _, tc := range cases {
		t.Run(tc.call, func(t *testing.T) {
			api := &fakeDocumentBrowser{}
			rec := serveBrowser(t, api, tc.method, tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			if api.lastCall != tc.call || api.project != "proj-doc1" {
				t.Errorf("reached %q for %q", api.lastCall, api.project)
			}
		})
	}
}

func TestDocumentBrowserPassesTheRequestThrough(t *testing.T) {
	api := &fakeDocumentBrowser{documents: []json.RawMessage{json.RawMessage(`{"_id":1}`)}}
	rec := serveBrowser(t, api, http.MethodGet,
		browserBase+"/databases/shop/collections/a%2Fb.c/documents?filter=%7B%22x%22%3A1%7D&sort=%7B%22x%22%3A-1%7D&projection=%7B%22x%22%3A1%7D&limit=5&skip=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if api.ns.Database != "shop" || api.ns.Collection != "a/b.c" {
		t.Errorf("namespace: %+v", api.ns)
	}
	want := docbrowser.FindRequest{Filter: `{"x":1}`, Sort: `{"x":-1}`, Projection: `{"x":1}`, Limit: 5, Skip: 10}
	if api.find != want {
		t.Errorf("find: %+v", api.find)
	}
	if !strings.Contains(rec.Body.String(), `"documents":[{"_id":1}]`) {
		t.Errorf("body: %s", rec.Body)
	}
}

func TestDocumentBrowserWritesCarryTheirBodyAndID(t *testing.T) {
	api := &fakeDocumentBrowser{}
	serveBrowser(t, api, http.MethodPut, browserBase+"/databases/shop/collections/orders/documents?id=%7B%22%24oid%22%3A%22x%22%7D", `{"a":2}`)
	if api.id != `{"$oid":"x"}` || api.body != `{"a":2}` {
		t.Errorf("id %q body %q", api.id, api.body)
	}
	rec := serveBrowser(t, api, http.MethodPost, browserBase+"/databases/shop/collections/orders/documents", `{"a":1}`)
	if !strings.Contains(rec.Body.String(), `"insertedId":{"$oid":"abc"}`) {
		t.Errorf("insert body: %s", rec.Body)
	}
	serveBrowser(t, api, http.MethodDelete, browserBase+"/databases/shop/collections/orders/indexes/by%20name", "")
	if api.index != "by name" {
		t.Errorf("index: %q", api.index)
	}
	serveBrowser(t, api, http.MethodDelete, browserBase+"/databases/shop/collections/50%25off", "")
	if api.ns.Collection != "50%off" {
		t.Errorf("a literal percent: %q", api.ns.Collection)
	}
}

func TestDocumentBrowserRejectsBadRequests(t *testing.T) {
	coll := browserBase + "/databases/shop/collections/orders"
	cases := map[string]struct{ method, path, body string }{
		"bad limit":      {http.MethodGet, coll + "/documents?limit=ten", ""},
		"bad skip":       {http.MethodGet, coll + "/documents?skip=-", ""},
		"bad project id": {http.MethodGet, "/api/projects/bad%20id/documentdb/databases", ""},
		"bad collection": {http.MethodPost, browserBase + "/databases/shop/collections", `{"name":`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			api := &fakeDocumentBrowser{}
			rec := serveBrowser(t, api, tc.method, tc.path, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
			}
			if api.lastCall != "" {
				t.Errorf("reached %s", api.lastCall)
			}
		})
	}
}

func TestDocumentBrowserRefusesAnOversizedDocument(t *testing.T) {
	api := &fakeDocumentBrowser{}
	body := `{"a":"` + strings.Repeat("x", maxDocumentBody) + `"}`
	rec := serveBrowser(t, api, http.MethodPost, browserBase+"/databases/shop/collections/orders/documents", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
}

func TestDocumentBrowserErrorStatuses(t *testing.T) {
	cases := []struct {
		err    error
		status int
		text   string
	}{
		{fmt.Errorf("%w: bad filter", docbrowser.ErrInvalid), 400, "bad filter"},
		{docbrowser.ErrProjectNotFound, 404, "project not found"},
		{docbrowser.ErrNotDocumentDB, 404, "DocumentDB"},
		{docbrowser.ErrDocumentNotFound, 404, "document not found"},
		{fmt.Errorf("%w (PAUSED)", docbrowser.ErrNotServable), 409, "not running"},
		{docbrowser.ErrGatewayNotReady, 503, "not serving"},
		{&docbrowser.QueryError{Message: "unknown operator: $x"}, 400, "unknown operator"},
		{&docbrowser.QueryError{Message: "E11000 duplicate key", Conflict: true}, 409, "duplicate"},
		{fmt.Errorf("%w: dial 10.0.0.1", docbrowser.ErrUnavailable), 502, "did not answer"},
		{fmt.Errorf("%w: ctx", docbrowser.ErrTimeout), 504, "in time"},
		{errors.New("vault: secret projects/x missing"), 500, "document browser failed"},
	}
	for _, tc := range cases {
		api := &fakeDocumentBrowser{err: tc.err}
		rec := serveBrowser(t, api, http.MethodGet, browserBase+"/databases", "")
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) {
			t.Errorf("%v: got %d %s, want %d containing %q", tc.err, rec.Code, rec.Body, tc.status, tc.text)
		}
		if strings.Contains(rec.Body.String(), "10.0.0.1") || strings.Contains(rec.Body.String(), "vault") {
			t.Errorf("%v: internal detail leaked: %s", tc.err, rec.Body)
		}
	}
}

func TestDocumentBrowserEveryRouteChecksTheProjectID(t *testing.T) {
	base := "/api/projects/bad%20id/documentdb/databases"
	coll := base + "/shop/collections/orders"
	routes := [][2]string{
		{http.MethodGet, base + "/shop/collections"}, {http.MethodPost, base + "/shop/collections"},
		{http.MethodDelete, coll}, {http.MethodGet, coll + "/documents"}, {http.MethodPost, coll + "/documents"},
		{http.MethodPut, coll + "/documents"}, {http.MethodPatch, coll + "/documents"}, {http.MethodDelete, coll + "/documents"},
		{http.MethodGet, coll + "/count"}, {http.MethodGet, coll + "/sample"}, {http.MethodGet, coll + "/indexes"},
		{http.MethodPost, coll + "/indexes"}, {http.MethodDelete, coll + "/indexes/a_1"},
	}
	for _, route := range routes {
		api := &fakeDocumentBrowser{}
		rec := serveBrowser(t, api, route[0], route[1], `{}`)
		if rec.Code != http.StatusBadRequest || api.lastCall != "" {
			t.Errorf("%s %s: %d, reached %q", route[0], route[1], rec.Code, api.lastCall)
		}
	}
}

// A path carrying an escape that does not decode is refused rather than
// passed on still escaped.
func TestDocumentBrowserRefusesUndecodableNames(t *testing.T) {
	coll := browserBase + "/databases/shop/collections/orders"
	cases := [][3]string{
		{http.MethodGet, browserBase + "/databases/shop/collections", browserBase + "/databases/sh%ZZ/collections"},
		{http.MethodPost, browserBase + "/databases/shop/collections", browserBase + "/databases/sh%ZZ/collections"},
		{http.MethodDelete, coll, browserBase + "/databases/shop/collections/or%ZZ"},
		{http.MethodDelete, coll + "/indexes/a_1", coll + "/indexes/a%ZZ"},
	}
	for _, tc := range cases {
		api := &fakeDocumentBrowser{}
		req := httptest.NewRequest(tc[0], tc[1], strings.NewReader(`{"name":"x"}`))
		req.URL.RawPath = tc[2]
		rec := httptest.NewRecorder()
		browserRouter(api).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || api.lastCall != "" {
			t.Errorf("%s %s: %d, reached %q", tc[0], tc[2], rec.Code, api.lastCall)
		}
	}
}

func TestDocumentBrowserWriteBodiesAreBounded(t *testing.T) {
	coll := browserBase + "/databases/shop/collections/orders"
	huge := strings.Repeat("x", maxDocumentBody+1)
	for _, route := range [][2]string{{http.MethodPut, coll + "/documents?id=1"}, {http.MethodPatch, coll + "/documents?id=1"}, {http.MethodPost, coll + "/indexes"}} {
		rec := serveBrowser(t, &fakeDocumentBrowser{}, route[0], route[1], huge)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s %s: %d, want 413", route[0], route[1], rec.Code)
		}
	}
}
