package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/xuri/excelize/v2"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/tableimport"
)

const importProject = "proj_import"

type fakeImportTarget struct {
	calls   int
	opts    tableimport.Options
	rows    [][]string
	bytes   int64
	loadErr error
}

func (f *fakeImportTarget) Load(_ context.Context, projectID string, opts tableimport.Options, src tableimport.RecordReader, estimated int64) (tableimport.Result, error) {
	f.calls++
	f.opts, f.bytes = opts, estimated
	for {
		rec, _, err := src.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return tableimport.Result{}, err
		}
		f.rows = append(f.rows, rec)
	}
	if f.loadErr != nil {
		return tableimport.Result{}, f.loadErr
	}
	return tableimport.Result{Rows: len(f.rows) - 1}, nil
}

type fakeSheets struct {
	calls int
	body  string
	err   error
}

func (f *fakeSheets) Fetch(_ context.Context, _ tableimport.SheetRef, _ int64) (io.ReadCloser, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func smallImportLimits() tableimport.Limits {
	lim, _ := tableimport.ForTier(domain.Free)
	lim.MaxBytes, lim.MaxRows = 16384, 100
	return lim
}

type importFixture struct {
	handler *TableImportHandler
	target  *fakeImportTarget
	sheets  *fakeSheets
	audit   *sdkKeyAudit
	router  chi.Router
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	f := &importFixture{target: &fakeImportTarget{}, sheets: &fakeSheets{body: "a,b\n1,2\n"}, audit: &sdkKeyAudit{}}
	f.handler = NewTableImportHandler(f.target, func(context.Context, string) (tableimport.Limits, error) {
		return smallImportLimits(), nil
	}, f.sheets, f.audit)
	router := chi.NewRouter()
	router.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Post("/import/preview", f.handler.Preview)
		r.Post("/import", f.handler.Import)
	})
	f.router = router
	return f
}

func (f *importFixture) do(req *http.Request) *httptest.ResponseRecorder {
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "user-42"}))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

type formPart struct{ name, filename, body string }

func multipartRequest(t *testing.T, path string, parts ...formPart) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		var part io.Writer
		var err error
		if p.filename != "" {
			part, err = w.CreateFormFile(p.name, p.filename)
		} else {
			part, err = w.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte(p.body))
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func previewPath() string { return "/api/schema/" + importProject + "/import/preview" }
func importPath() string  { return "/api/schema/" + importProject + "/import" }

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}

func TestImportPreview_CSVSuggestsColumnsAndTypes(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, previewPath(),
		formPart{name: "hasHeader", body: "true"},
		formPart{name: "file", filename: "people.xlsx", body: "Name,Age\nann,31\nbob,40\n"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Format    string                      `json:"format"`
		Delimiter string                      `json:"delimiter"`
		Columns   []tableimport.PreviewColumn `json:"columns"`
		Rows      [][]string                  `json:"rows"`
		Limits    tableimport.Limits          `json:"limits"`
	}
	decode(t, rec, &got)
	if got.Format != "csv" || got.Delimiter != "," || len(got.Columns) != 2 || got.Columns[1].Type != tableimport.TypeInteger {
		t.Fatalf("preview = %+v", got)
	}
	if got.Limits.MaxBytes != 16384 {
		t.Fatalf("limits = %+v", got.Limits)
	}
}

func xlsxBytes(t *testing.T) []byte {
	t.Helper()
	book := excelize.NewFile()
	_ = book.SetSheetRow("Sheet1", "A1", &[]any{"sku", "qty"})
	_ = book.SetSheetRow("Sheet1", "A2", &[]any{"A-1", 5})
	_, _ = book.NewSheet("Second")
	var buf bytes.Buffer
	if err := book.Write(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestImportPreview_XLSXListsSheets(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, previewPath(),
		formPart{name: "hasHeader", body: "true"},
		formPart{name: "file", filename: "book.csv", body: string(xlsxBytes(t))}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Format  string                      `json:"format"`
		Sheets  []string                    `json:"sheets"`
		Sheet   string                      `json:"sheet"`
		Columns []tableimport.PreviewColumn `json:"columns"`
	}
	decode(t, rec, &got)
	if got.Format != "xlsx" || len(got.Sheets) != 2 || got.Sheet != "Sheet1" || got.Columns[1].Type != tableimport.TypeInteger {
		t.Fatalf("preview = %+v", got)
	}
}

func TestImportPreview_RefusesBinaryAndOversizedFiles(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, previewPath(), formPart{name: "file", filename: "a.csv", body: "\x7fELF\x00\x00\x00"}))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("binary: status %d", rec.Code)
	}
	big := "a\n" + strings.Repeat("xxxxxxxxx\n", 2000)
	rec = f.do(multipartRequest(t, importPath(),
		formPart{name: "options", body: `{"schema":"public","table":"t","mode":"create","hasHeader":true,"columns":[{"source":0,"name":"a","type":"text"}]}`},
		formPart{name: "file", filename: "a.csv", body: big}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: status %d: %s", rec.Code, rec.Body)
	}
}

func TestImportPreview_SheetsURLIsParsedBeforeAnyFetch(t *testing.T) {
	f := newImportFixture(t)
	for _, bad := range []string{"http://169.254.169.254/latest/meta-data/", "https://evil.example/x.csv", "file:///etc/passwd"} {
		body, _ := json.Marshal(map[string]any{"sheetsUrl": bad, "hasHeader": true})
		req := httptest.NewRequest(http.MethodPost, previewPath(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if rec := f.do(req); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", bad, rec.Code)
		}
	}
	if f.sheets.calls != 0 {
		t.Fatalf("a non-Google URL reached the fetcher %d times", f.sheets.calls)
	}
	body, _ := json.Marshal(map[string]any{"sheetsUrl": "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit#gid=0", "hasHeader": true})
	req := httptest.NewRequest(http.MethodPost, previewPath(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := f.do(req)
	if rec.Code != http.StatusOK || f.sheets.calls != 1 {
		t.Fatalf("status %d calls %d: %s", rec.Code, f.sheets.calls, rec.Body)
	}
}

func TestImportPreview_APrivateSheetIsExplained(t *testing.T) {
	f := newImportFixture(t)
	f.sheets.err = tableimport.ErrSheetNotPublic
	body, _ := json.Marshal(map[string]any{"sheetsUrl": "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit"})
	req := httptest.NewRequest(http.MethodPost, previewPath(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := f.do(req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "anyone with the link") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

const validImportOptions = `{"schema":"public","table":"people","mode":"create","hasHeader":true,
	"columns":[{"source":0,"name":"name","type":"text"},{"source":1,"name":"age","type":"integer"}]}`

func TestImport_LoadsWithTheValidatedOptionsAndAudits(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, importPath(),
		formPart{name: "options", body: validImportOptions},
		formPart{name: "file", filename: "p.csv", body: "Name,Age\nann,31\n=cmd|' /C calc'!A0,2\n"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.target.calls != 1 || f.target.opts.Table != "people" || len(f.target.rows) != 3 {
		t.Fatalf("target = %+v", f.target)
	}
	if f.target.rows[2][0] != "=cmd|' /C calc'!A0" {
		t.Fatalf("formula cell changed: %q", f.target.rows[2][0])
	}
	if len(f.audit.entries) != 1 {
		t.Fatalf("audit entries = %d", len(f.audit.entries))
	}
	entry := f.audit.entries[0]
	if entry.Action != "table.import" || entry.ResourceID != importProject || entry.UserID != "user-42" ||
		!strings.Contains(entry.Details, `"table":"people"`) || !strings.Contains(entry.Details, `"outcome":"imported"`) {
		t.Fatalf("audit = %+v", entry)
	}
}

func TestImport_RefusesInvalidOptionsWithoutLoading(t *testing.T) {
	f := newImportFixture(t)
	for _, opts := range []string{
		`{"schema":"public","table":"x\"; DROP TABLE y;--","mode":"create","columns":[{"source":0,"name":"a","type":"text"}]}`,
		`{"schema":"excalibase","table":"t","mode":"create","columns":[{"source":0,"name":"a","type":"text"}]}`,
		`{"schema":"public","table":"t","mode":"create","columns":[{"source":0,"name":"a","type":"text; DROP"}]}`,
		`not json`,
	} {
		rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: opts}, formPart{name: "file", filename: "a.csv", body: "a\n1\n"}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", opts, rec.Code)
		}
	}
	if f.target.calls != 0 {
		t.Fatalf("loader called %d times", f.target.calls)
	}
}

func TestImport_OptionsMustComeBeforeTheFile(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "file", filename: "a.csv", body: "a\n1\n"}, formPart{name: "options", body: validImportOptions}))
	if rec.Code != http.StatusBadRequest || f.target.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, f.target.calls)
	}
}

func TestImport_RowErrorsAreReportedWithLines(t *testing.T) {
	f := newImportFixture(t)
	f.target.loadErr = &tableimport.RowErrors{Errors: []tableimport.RowError{{Line: 3, Column: "age", Value: "x", Message: "integer is not a whole number in range"}}}
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "a.csv", body: "Name,Age\na,1\nb,x\n"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d", rec.Code)
	}
	var got struct {
		RowErrors []tableimport.RowError `json:"rowErrors"`
	}
	decode(t, rec, &got)
	if len(got.RowErrors) != 1 || got.RowErrors[0].Line != 3 {
		t.Fatalf("body = %s", rec.Body)
	}
	if !strings.Contains(f.audit.entries[0].Details, `"outcome":"refused"`) {
		t.Fatalf("audit = %+v", f.audit.entries[0])
	}
}

func TestImport_StatusForEachRefusal(t *testing.T) {
	cases := map[error]int{
		tableimport.ErrTableExists:                  http.StatusConflict,
		tableimport.ErrTableMissing:                 http.StatusUnprocessableEntity,
		tableimport.ErrDiskFull:                     http.StatusInsufficientStorage,
		tableimport.ErrTooManyRows:                  http.StatusRequestEntityTooLarge,
		tableimport.ErrTimedOut:                     http.StatusUnprocessableEntity,
		errors.New("pq: connection refused secret"): http.StatusInternalServerError,
	}
	for loadErr, want := range cases {
		f := newImportFixture(t)
		f.target.loadErr = loadErr
		rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "a.csv", body: "Name,Age\na,1\n"}))
		if rec.Code != want {
			t.Errorf("%v: status %d, want %d", loadErr, rec.Code, want)
		}
		if strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("internal error leaked: %s", rec.Body)
		}
	}
}

func TestImport_AnUnresolvedPlanRefuses(t *testing.T) {
	f := newImportFixture(t)
	f.handler.limits = func(context.Context, string) (tableimport.Limits, error) {
		return tableimport.Limits{}, errors.New("no plan")
	}
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "a.csv", body: "a\n"}))
	if rec.Code != http.StatusServiceUnavailable || f.target.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, f.target.calls)
	}
}

func TestImport_ConcurrentImportsAreBounded(t *testing.T) {
	f := newImportFixture(t)
	for i := 0; i < cap(f.handler.slots); i++ {
		f.handler.slots <- struct{}{}
	}
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "a.csv", body: "a\n"}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestDiskBytes(t *testing.T) {
	cases := map[string]int64{"5Gi": 5 << 30, "500Mi": 500 << 20, "": 0, "lots": 0}
	for size, want := range cases {
		if got := diskBytes(size); got != want {
			t.Errorf("diskBytes(%q) = %d, want %d", size, got, want)
		}
	}
}

// An import is spooled whole before the load, so the disk check sees the
// real size and no temp file outlives the request.
func TestImport_SpoolsTheFileAndCleansUp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	f := newImportFixture(t)
	body := "Name,Age\nann,31\nbob,40\n"
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "p.csv", body: body}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if f.target.bytes != int64(len(body)) {
		t.Fatalf("estimated bytes = %d, want %d", f.target.bytes, len(body))
	}
	book := xlsxBytes(t)
	opts := `{"schema":"public","table":"stock","mode":"create","hasHeader":true,"columns":[{"source":0,"name":"sku","type":"text"},{"source":1,"name":"qty","type":"integer"}]}`
	rec = f.do(multipartRequest(t, importPath(), formPart{name: "options", body: opts}, formPart{name: "file", filename: "b.xlsx", body: string(book)}))
	if rec.Code != http.StatusCreated || f.target.rows[len(f.target.rows)-1][0] != "A-1" {
		t.Fatalf("xlsx status %d rows %q: %s", rec.Code, f.target.rows, rec.Body)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestImport_FromAGoogleSheet(t *testing.T) {
	f := newImportFixture(t)
	f.sheets.body = "Name,Age\nann,31\n"
	body, _ := json.Marshal(map[string]any{
		"sheetsUrl": "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit#gid=0",
		"options":   json.RawMessage(validImportOptions),
	})
	req := httptest.NewRequest(http.MethodPost, importPath(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := f.do(req)
	if rec.Code != http.StatusCreated || f.sheets.calls != 1 || len(f.target.rows) != 2 {
		t.Fatalf("status %d calls %d rows %q: %s", rec.Code, f.sheets.calls, f.target.rows, rec.Body)
	}
	if !strings.Contains(f.audit.entries[0].Details, `"source":"google_sheets"`) {
		t.Fatalf("audit = %s", f.audit.entries[0].Details)
	}
}

// closingSheets fails any read after Close, like a real response body.
type closingSheets struct{ body string }

type closeTrackingBody struct {
	r      io.Reader
	closed bool
}

func (b *closeTrackingBody) Read(p []byte) (int, error) {
	if b.closed {
		return 0, errors.New("read on closed body")
	}
	return b.r.Read(p)
}
func (b *closeTrackingBody) Close() error { b.closed = true; return nil }

func (c closingSheets) Fetch(context.Context, tableimport.SheetRef, int64) (io.ReadCloser, error) {
	return &closeTrackingBody{r: strings.NewReader(c.body)}, nil
}

// A sheet larger than one read buffer is still previewed from an open body.
func TestImportPreview_ALargeSheetIsReadBeforeItsBodyCloses(t *testing.T) {
	f := newImportFixture(t)
	f.handler.sheets = closingSheets{body: "a,b\n" + strings.Repeat("1,"+strings.Repeat("y", 200)+"\n", 60)}
	body, _ := json.Marshal(map[string]any{"sheetsUrl": "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit", "hasHeader": true})
	req := httptest.NewRequest(http.MethodPost, previewPath(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if rec := f.do(req); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// Fields after the options cannot change how the file is read.
func TestImport_FieldsAfterTheOptionsAreIgnored(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, importPath(),
		formPart{name: "options", body: validImportOptions},
		formPart{name: "delimiter", body: ";"},
		formPart{name: "file", filename: "a.csv", body: "Name,Age\nann,31\n"}))
	if rec.Code != http.StatusCreated || len(f.target.rows[1]) != 2 {
		t.Fatalf("status %d rows %q", rec.Code, f.target.rows)
	}
}

// One project cannot hold every import slot of a replica.
func TestImport_OneImportPerProjectAtATime(t *testing.T) {
	f := newImportFixture(t)
	if !f.handler.claimProject(importProject) {
		t.Fatal("first claim refused")
	}
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: validImportOptions}, formPart{name: "file", filename: "a.csv", body: "a\n"}))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d", rec.Code)
	}
	f.handler.releaseProject(importProject)
}

func TestSweepSpoolFiles_RemovesLeftovers(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(dir+"/excalibase-import-123", []byte("x"), 0o600)
	_ = os.WriteFile(dir+"/other", []byte("x"), 0o600)
	SweepImportSpool(dir)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "other" {
		t.Fatalf("entries = %v", entries)
	}
}

func TestImport_RequestShapeIsChecked(t *testing.T) {
	f := newImportFixture(t)
	rec := f.do(multipartRequest(t, previewPath(), formPart{name: "delimiter", body: "x"}, formPart{name: "file", filename: "a.csv", body: "a;b\n1;2\n"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown delimiter: status %d", rec.Code)
	}
	rec = f.do(multipartRequest(t, previewPath(), formPart{name: "delimiter", body: ";"}, formPart{name: "file", filename: "a.csv", body: "a;b\n1;2\n"}))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delimiter":";"`) {
		t.Errorf("semicolon: status %d %s", rec.Code, rec.Body)
	}
	rec = f.do(multipartRequest(t, previewPath(), formPart{name: "file", filename: "a.tsv", body: "a\tb\n1\t2\n"}))
	if !strings.Contains(rec.Body.String(), `"delimiter":"tab"`) {
		t.Errorf("tab: %s", rec.Body)
	}
	req := httptest.NewRequest(http.MethodPost, previewPath(), strings.NewReader("a,b"))
	req.Header.Set("Content-Type", "text/csv")
	if rec := f.do(req); rec.Code != http.StatusBadRequest {
		t.Errorf("raw body: status %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, importPath(), strings.NewReader(`{"sheetsUrl":"https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"}`))
	req.Header.Set("Content-Type", "application/json")
	if rec := f.do(req); rec.Code != http.StatusBadRequest {
		t.Errorf("sheet import without options: status %d", rec.Code)
	}
	rec = f.do(multipartRequest(t, previewPath(), formPart{name: "hasHeader", body: "true"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no file: status %d", rec.Code)
	}
}

func TestImport_AWorkbookOverItsOwnCapIsRefused(t *testing.T) {
	f := newImportFixture(t)
	f.handler.limits = func(context.Context, string) (tableimport.Limits, error) {
		lim := smallImportLimits()
		lim.MaxXLSXBytes = 1024
		return lim, nil
	}
	opts := `{"schema":"public","table":"stock","mode":"create","hasHeader":true,"columns":[{"source":0,"name":"sku","type":"text"}]}`
	rec := f.do(multipartRequest(t, importPath(), formPart{name: "options", body: opts}, formPart{name: "file", filename: "b.xlsx", body: string(xlsxBytes(t))}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	rec = f.do(multipartRequest(t, previewPath(), formPart{name: "file", filename: "b.xlsx", body: string(xlsxBytes(t))}))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("preview status %d: %s", rec.Code, rec.Body)
	}
}

func TestImportStatus_SheetDownloadFailuresAreBadGateway(t *testing.T) {
	status, known := importStatus(fmt.Errorf("%w: timeout", tableimport.ErrSheetDownload))
	if status != http.StatusBadGateway || !known {
		t.Fatalf("status %d", status)
	}
	if importErrorMessage(&http.MaxBytesError{Limit: 1}) != tableimport.ErrFileTooLarge.Error() {
		t.Fatal("an oversized body is not reported as too large")
	}
}

func sheetsRequestFor(t *testing.T, path string, extra map[string]any) *http.Request {
	t.Helper()
	body := map[string]any{"sheetsUrl": "https://docs.google.com/spreadsheets/d/1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms/edit", "hasHeader": true}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestImportSheets_RefusalsAndOddAnswers(t *testing.T) {
	f := newImportFixture(t)
	f.sheets.body = string(xlsxBytes(t))
	if rec := f.do(sheetsRequestFor(t, previewPath(), nil)); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("a sheet answering a workbook: status %d", rec.Code)
	}
	f.sheets.err = fmt.Errorf("%w: reset", tableimport.ErrSheetDownload)
	if rec := f.do(sheetsRequestFor(t, previewPath(), nil)); rec.Code != http.StatusBadGateway {
		t.Errorf("download failure: status %d", rec.Code)
	}
	f.handler.sheets = nil
	if rec := f.do(sheetsRequestFor(t, previewPath(), nil)); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no fetcher: status %d", rec.Code)
	}
	bad := json.RawMessage(`{"schema":"pg_catalog","table":"t","mode":"create","columns":[{"source":0,"name":"a","type":"text"}]}`)
	if rec := f.do(sheetsRequestFor(t, importPath(), map[string]any{"options": bad})); rec.Code != http.StatusBadRequest {
		t.Errorf("bad options: status %d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, previewPath(), strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	if rec := f.do(req); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: status %d", rec.Code)
	}
}

func TestImport_DamagedWorkbooksAreRefused(t *testing.T) {
	f := newImportFixture(t)
	for _, body := range []string{"PK\x03\x04not really a zip", "PK\x03\x04" + strings.Repeat("\x00", 64)} {
		rec := f.do(multipartRequest(t, previewPath(), formPart{name: "file", filename: "x.xlsx", body: body}))
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("damaged workbook: status %d: %s", rec.Code, rec.Body)
		}
	}
	rec := f.do(multipartRequest(t, previewPath(), formPart{name: "sheet", body: "Nope"}, formPart{name: "file", filename: "b.xlsx", body: string(xlsxBytes(t))}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing sheet: status %d", rec.Code)
	}
	opts := `{"schema":"public","table":"t","mode":"create","hasHeader":true,"columns":[{"source":0,"name":"a","type":"text"}]}`
	rec = f.do(multipartRequest(t, importPath(), formPart{name: "options", body: opts}, formPart{name: "file", filename: "e.csv", body: ""}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty file: status %d", rec.Code)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("client went away") }

func TestSpool_AReadFailureLeavesNoFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, _, err := spool(failingReader{}, 10); err == nil {
		t.Fatal("a failed read was spooled")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
	if uploadError(errors.New("x"), "fallback").Error() != "fallback" {
		t.Fatal("fallback message lost")
	}
}
