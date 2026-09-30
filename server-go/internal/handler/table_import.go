package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/tableimport"
)

// Studio's table import (EXC-368): POST /api/schema/{projectId}/import/preview
// reads the first rows and suggests columns; POST .../import creates or
// appends to a table in one transaction. Both parse the file here; nothing
// the browser says about it is believed.

const (
	auditActionTableImport = "table.import"
	auditResourceTable     = "table"
	// importSlots bounds concurrent imports per replica: a workbook is
	// parsed in memory, and the control plane runs in 512 MiB.
	importSlots    = 2
	importDeadline = 15 * time.Minute
	// uploadDeadline is how long the whole request body may take to arrive.
	uploadDeadline = 10 * time.Minute
	spoolPattern   = "excalibase-import-"
	// multipartSlack covers the form's boundaries and the options part.
	multipartSlack   = 1 << 20
	maxOptionsBytes  = 256 << 10
	sourceUpload     = "upload"
	sourceSheets     = "google_sheets"
	formFieldFile    = "file"
	formFieldOptions = "options"
)

// ImportTarget loads rows into a project's database.
type ImportTarget interface {
	Load(ctx context.Context, projectID string, opts tableimport.Options, src tableimport.Source, estimatedBytes int64) (tableimport.Result, error)
}

// ImportLimits is a project's import allowance, from its plan.
type ImportLimits func(ctx context.Context, projectID string) (tableimport.Limits, error)

// SheetsSource downloads a Google Sheet's CSV export.
type SheetsSource interface {
	Fetch(ctx context.Context, ref tableimport.SheetRef, maxBytes int64) (io.ReadCloser, error)
}

type TableImportHandler struct {
	target ImportTarget
	limits ImportLimits
	sheets SheetsSource
	audit  auditWriter
	slots  chan struct{}
	// busy holds the projects with an import or preview running, so one
	// project cannot hold every slot of the replica.
	mu   sync.Mutex
	busy map[string]bool
}

func NewTableImportHandler(target ImportTarget, limits ImportLimits, sheets SheetsSource, audit auditWriter) *TableImportHandler {
	return &TableImportHandler{target: target, limits: limits, sheets: sheets, audit: audit,
		slots: make(chan struct{}, importSlots), busy: map[string]bool{}}
}

func (h *TableImportHandler) claimProject(projectID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy[projectID] {
		return false
	}
	h.busy[projectID] = true
	return true
}

func (h *TableImportHandler) releaseProject(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.busy, projectID)
}

// SweepImportSpool removes spool files a crashed process left in dir.
func SweepImportSpool(dir string) {
	leftovers, _ := filepath.Glob(filepath.Join(dir, spoolPattern+"*"))
	for _, path := range leftovers {
		if err := os.Remove(path); err != nil {
			log.Printf("WARN: remove import spool %s: %v", path, err)
		}
	}
}

// readSettings are what the reader needs to find rows: which sheet, which
// separator, whether the first row is a header.
type readSettings struct {
	HasHeader  bool     `json:"hasHeader"`
	Delimiter  string   `json:"delimiter,omitempty"`
	Sheet      string   `json:"sheet,omitempty"`
	NullTokens []string `json:"nullTokens,omitempty"`
}

type sheetsRequest struct {
	SheetsURL string               `json:"sheetsUrl"`
	Options   *tableimport.Options `json:"options,omitempty"`
	readSettings
}

// openedSource is a parsed file ready to be read row by row.
type openedSource struct {
	src       tableimport.Source
	format    tableimport.Format
	delimiter rune
	sheets    []string
	sheet     string
	origin    string
	// size is the spooled file's length; 0 when the file was streamed.
	size  int64
	close func()
}

type previewResponse struct {
	tableimport.PreviewResult
	Format    tableimport.Format `json:"format"`
	Delimiter string             `json:"delimiter,omitempty"`
	Sheets    []string           `json:"sheets,omitempty"`
	Sheet     string             `json:"sheet,omitempty"`
	HasHeader bool               `json:"hasHeader"`
	Limits    tableimport.Limits `json:"limits"`
}

// acquire takes a replica slot and the project's own claim, and bounds how
// long the upload may take to arrive, so a trickled body cannot hold them.
func (h *TableImportHandler) acquire(w http.ResponseWriter, r *http.Request) bool {
	projectID := chi.URLParam(r, "projectId")
	if !h.claimProject(projectID) {
		httpError(w, "an import into this project is already running; try again when it finishes", http.StatusTooManyRequests)
		return false
	}
	select {
	case h.slots <- struct{}{}:
	default:
		h.releaseProject(projectID)
		httpError(w, "another import is running; try again in a moment", http.StatusTooManyRequests)
		return false
	}
	// Not every writer supports deadlines (tests' recorders); the edge's own timeout still applies then.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadDeadline))
	return true
}

func (h *TableImportHandler) release(r *http.Request) {
	<-h.slots
	h.releaseProject(chi.URLParam(r, "projectId"))
}

func (h *TableImportHandler) projectLimits(w http.ResponseWriter, r *http.Request) (tableimport.Limits, bool) {
	lim, err := h.limits(r.Context(), chi.URLParam(r, "projectId"))
	if err != nil {
		log.Printf("ERROR: import limits for %s: %v", safeLog(chi.URLParam(r, "projectId")), err)
		httpError(w, "the project's plan could not be resolved; try again", http.StatusServiceUnavailable)
		return tableimport.Limits{}, false
	}
	return lim, true
}

// Preview answers the first rows with suggested names and types.
func (h *TableImportHandler) Preview(w http.ResponseWriter, r *http.Request) {
	lim, ok := h.projectLimits(w, r)
	if !ok || !h.acquire(w, r) {
		return
	}
	defer h.release(r)
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxBytes+multipartSlack)
	opened, settings, _, err := h.openRequest(r, lim, false)
	if err != nil {
		writeImportError(w, err)
		return
	}
	defer opened.close()
	result, err := tableimport.Preview(opened.src, tableimport.PreviewOptions{HasHeader: settings.HasHeader, NullTokens: settings.NullTokens})
	if err != nil {
		writeImportError(w, err)
		return
	}
	writeJSON(w, previewResponse{
		PreviewResult: result, Format: opened.format, Delimiter: delimiterName(opened),
		Sheets: opened.sheets, Sheet: opened.sheet, HasHeader: settings.HasHeader, Limits: lim,
	})
}

// Import validates the chosen options and loads the whole file.
func (h *TableImportHandler) Import(w http.ResponseWriter, r *http.Request) {
	lim, ok := h.projectLimits(w, r)
	if !ok || !h.acquire(w, r) {
		return
	}
	defer h.release(r)
	r.Body = http.MaxBytesReader(w, r.Body, lim.MaxBytes+multipartSlack)
	opened, _, opts, err := h.openRequest(r, lim, true)
	if err != nil {
		writeImportError(w, err)
		return
	}
	defer opened.close()
	ctx, cancel := context.WithTimeout(r.Context(), importDeadline)
	defer cancel()
	result, err := h.target.Load(ctx, chi.URLParam(r, "projectId"), *opts, opened.src, opened.size)
	h.record(r, *opts, opened, result, err)
	if err != nil {
		writeImportError(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"schema": opts.Schema, "table": opts.Table, "mode": opts.Mode, "rows": result.Rows,
	})
}

// openRequest reads a multipart upload or a JSON Google Sheets request. For an
// import (needOptions) the options must come first and are validated before a
// byte of the file is read, and the whole file is spooled to disk before the
// load: the tenant ends a transaction left idle for its statement timeout, so
// a slow upload must never be read while one is open.
func (h *TableImportHandler) openRequest(r *http.Request, lim tableimport.Limits, needOptions bool) (*openedSource, readSettings, *tableimport.Options, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, readSettings{}, nil, badRequest("send the file as multipart/form-data or a Google Sheets link as JSON")
	}
	if mediaType == "application/json" {
		return h.openSheets(r, lim, needOptions)
	}
	if mediaType != "multipart/form-data" {
		return nil, readSettings{}, nil, badRequest("send the file as multipart/form-data or a Google Sheets link as JSON")
	}
	return h.openUpload(r, lim, needOptions)
}

func (h *TableImportHandler) openSheets(r *http.Request, lim tableimport.Limits, needOptions bool) (*openedSource, readSettings, *tableimport.Options, error) {
	var req sheetsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOptionsBytes)).Decode(&req); err != nil {
		return nil, readSettings{}, nil, badRequest("invalid request body")
	}
	settings := req.readSettings
	if needOptions {
		if req.Options == nil {
			return nil, settings, nil, badRequest("options are required")
		}
		if err := req.Options.Validate(lim); err != nil {
			return nil, settings, nil, badRequest(err.Error())
		}
		settings = settingsOf(*req.Options)
	}
	ref, err := tableimport.ParseSheetsURL(req.SheetsURL)
	if err != nil {
		return nil, settings, nil, badRequest(err.Error())
	}
	if h.sheets == nil {
		return nil, settings, nil, errSheetsUnavailable
	}
	body, err := h.sheets.Fetch(r.Context(), ref, lim.MaxBytes)
	if err != nil {
		return nil, settings, nil, err
	}
	opened, err := openFile(body, lim, settings, needOptions)
	if err != nil {
		body.Close()
		return nil, settings, nil, err
	}
	if opened.format != tableimport.FormatCSV {
		opened.close()
		body.Close()
		return nil, settings, nil, tableimport.ErrUnsupportedFormat
	}
	// A streamed preview still reads from the body; close it with the source.
	closeFile := opened.close
	opened.close = func() { closeFile(); body.Close() }
	opened.origin = sourceSheets
	return opened, settings, req.Options, nil
}

func settingsOf(opts tableimport.Options) readSettings {
	return readSettings{HasHeader: opts.HasHeader, Delimiter: opts.Delimiter, Sheet: opts.Sheet, NullTokens: opts.NullTokens}
}

// openUpload walks the form in order: small fields first, the file last,
// so the file can stream straight into the parser.
func (h *TableImportHandler) openUpload(r *http.Request, lim tableimport.Limits, needOptions bool) (*openedSource, readSettings, *tableimport.Options, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, readSettings{}, nil, badRequest("send the file as multipart/form-data")
	}
	var settings readSettings
	var opts *tableimport.Options
	for {
		part, err := reader.NextPart()
		if err != nil {
			return nil, settings, nil, uploadError(err, "the form has no file")
		}
		if part.FormName() == formFieldFile {
			if needOptions && opts == nil {
				return nil, settings, nil, badRequest("send the options before the file")
			}
			opened, err := openFile(part, lim, settings, needOptions)
			return opened, settings, opts, err
		}
		if err := readField(part, &settings, &opts, lim); err != nil {
			return nil, settings, nil, err
		}
	}
}

func uploadError(err error, fallback string) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return tableimport.ErrFileTooLarge
	}
	return badRequest(fallback)
}

func readField(part *multipart.Part, settings *readSettings, opts **tableimport.Options, lim tableimport.Limits) error {
	value, err := io.ReadAll(io.LimitReader(part, maxOptionsBytes))
	if err != nil {
		return uploadError(err, "the form could not be read")
	}
	if *opts != nil && part.FormName() != formFieldOptions {
		return nil // the validated options already say how to read the file
	}
	switch part.FormName() {
	case "hasHeader":
		settings.HasHeader = string(value) == "true"
	case "delimiter":
		settings.Delimiter = string(value)
	case "sheet":
		settings.Sheet = string(value)
	case formFieldOptions:
		var parsed tableimport.Options
		if err := json.Unmarshal(value, &parsed); err != nil {
			return badRequest("options are not valid JSON")
		}
		if err := parsed.Validate(lim); err != nil {
			return badRequest(err.Error())
		}
		*opts = &parsed
		*settings = settingsOf(parsed)
	}
	return nil
}

// openFile decides the format from the file's own bytes. With spoolAll the
// whole file is written to disk first; otherwise a CSV is streamed and only a
// workbook (which needs random access) is spooled.
func openFile(body io.Reader, lim tableimport.Limits, settings readSettings, spoolAll bool) (*openedSource, error) {
	if spoolAll {
		return spoolAndOpen(body, lim.MaxBytes, lim, settings)
	}
	buffered := bufio.NewReaderSize(body, tableimport.SniffBytes)
	head, err := buffered.Peek(tableimport.SniffBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, uploadError(err, "the file could not be read")
	}
	format, err := tableimport.DetectFormat(head)
	if err != nil {
		return nil, err
	}
	if format == tableimport.FormatXLSX {
		return spoolAndOpen(buffered, lim.MaxXLSXBytes, lim, settings)
	}
	return openCSV(buffered, head, 0, func() {}, lim, settings)
}

func spoolAndOpen(body io.Reader, max int64, lim tableimport.Limits, settings readSettings) (*openedSource, error) {
	file, size, err := spool(body, max)
	if err != nil {
		return nil, err
	}
	opened, err := openSpooled(file, size, lim, settings)
	if err != nil {
		discard(file)
		return nil, err
	}
	return opened, nil
}

// openSpooled opens a file already on disk; its close removes it.
func openSpooled(file *os.File, size int64, lim tableimport.Limits, settings readSettings) (*openedSource, error) {
	head := make([]byte, tableimport.SniffBytes)
	n, err := file.ReadAt(head, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read spooled upload: %w", err)
	}
	format, err := tableimport.DetectFormat(head[:n])
	if err != nil {
		return nil, err
	}
	if format == tableimport.FormatXLSX {
		if size > lim.MaxXLSXBytes {
			return nil, tableimport.ErrFileTooLarge
		}
		return openWorkbook(file, size, lim, settings.Sheet)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind spooled upload: %w", err)
	}
	return openCSV(file, head[:n], size, func() { discard(file) }, lim, settings)
}

func openCSV(body io.Reader, head []byte, size int64, closeFn func(), lim tableimport.Limits, settings readSettings) (*openedSource, error) {
	delimiter := tableimport.SniffDelimiter(head)
	if settings.Delimiter != "" {
		var err error
		if delimiter, err = tableimport.ParseDelimiter(settings.Delimiter); err != nil {
			return nil, badRequest(err.Error())
		}
	}
	src, err := tableimport.NewCSVSource(body, delimiter, lim)
	if err != nil {
		return nil, err
	}
	return &openedSource{src: src, format: tableimport.FormatCSV, delimiter: delimiter, origin: sourceUpload, size: size, close: closeFn}, nil
}

// spool copies body to a private temp file (0600), refusing past max.
func spool(body io.Reader, max int64) (*os.File, int64, error) {
	file, err := os.CreateTemp("", spoolPattern+"*")
	if err != nil {
		return nil, 0, fmt.Errorf("spool upload: %w", err)
	}
	written, err := io.Copy(file, io.LimitReader(body, max+1))
	if err != nil {
		discard(file)
		return nil, 0, uploadError(err, "the file could not be read")
	}
	if written > max {
		discard(file)
		return nil, 0, tableimport.ErrFileTooLarge
	}
	return file, written, nil
}

func discard(file *os.File) {
	_ = file.Close()
	_ = os.Remove(file.Name())
}

// openWorkbook reads a spooled workbook; a zip needs random access.
func openWorkbook(file *os.File, size int64, lim tableimport.Limits, sheet string) (*openedSource, error) {
	book, err := tableimport.OpenXLSX(file.Name(), lim)
	if err != nil {
		discard(file)
		return nil, err
	}
	src, err := book.Source(sheet)
	if err != nil {
		_ = book.Close()
		discard(file)
		return nil, err
	}
	chosen := sheet
	if chosen == "" {
		chosen = book.Sheets()[0]
	}
	return &openedSource{
		src: src, format: tableimport.FormatXLSX, sheets: book.Sheets(), sheet: chosen, origin: sourceUpload, size: size,
		close: func() { _ = book.Close(); discard(file) },
	}, nil
}

func delimiterName(opened *openedSource) string {
	switch {
	case opened.format != tableimport.FormatCSV:
		return ""
	case opened.delimiter == '\t':
		return "tab"
	default:
		return string(opened.delimiter)
	}
}

func (h *TableImportHandler) record(r *http.Request, opts tableimport.Options, opened *openedSource, result tableimport.Result, loadErr error) {
	if h.audit == nil {
		return
	}
	details := map[string]any{
		"schema": opts.Schema, "table": opts.Table, "mode": opts.Mode, "format": opened.format,
		"source": opened.origin, "rows": result.Rows, "outcome": "imported",
	}
	if loadErr != nil {
		details["outcome"] = "refused"
		details["reason"] = importErrorMessage(loadErr)
	}
	encoded, _ := json.Marshal(details)
	now := time.Now()
	projectID := chi.URLParam(r, "projectId")
	entry := &domain.AuditEntry{
		Action: auditActionTableImport, Resource: auditResourceTable, ResourceID: projectID,
		Details: string(encoded), IPAddress: clientIP(r), Timestamp: &now,
	}
	if user := auth.GetUser(r.Context()); user != nil {
		entry.UserID = user.ID
	}
	if err := h.audit.LogAudit(r.Context(), entry); err != nil {
		log.Printf("ERROR: audit %s on %s: %v", auditActionTableImport, safeLog(projectID), err)
	}
}

type requestError struct{ msg string }

func (e *requestError) Error() string { return e.msg }

func badRequest(msg string) error { return &requestError{msg: msg} }

var errSheetsUnavailable = errors.New("importing from Google Sheets is not available on this platform")

// importStatus maps a refusal to its status; anything unknown is a 500 with
// no detail.
func importStatus(err error) (int, bool) {
	var reqErr *requestError
	var fileErr *tableimport.FileError
	var rowErrs *tableimport.RowErrors
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		return http.StatusRequestEntityTooLarge, true
	case errors.Is(err, tableimport.ErrSheetDownload):
		return http.StatusBadGateway, true
	case errors.As(err, &reqErr), errors.Is(err, tableimport.ErrNotASheetsURL):
		return http.StatusBadRequest, true
	case errors.Is(err, tableimport.ErrFileTooLarge), errors.Is(err, tableimport.ErrTooManyRows), errors.Is(err, tableimport.ErrRecordTooLarge):
		return http.StatusRequestEntityTooLarge, true
	case errors.Is(err, tableimport.ErrUnsupportedFormat), errors.Is(err, tableimport.ErrActiveContent), errors.Is(err, tableimport.ErrZipBomb):
		return http.StatusUnsupportedMediaType, true
	case errors.Is(err, tableimport.ErrTableExists):
		return http.StatusConflict, true
	case errors.Is(err, projectdb.ErrNotServable), errors.Is(err, domain.ErrNoDatabase):
		return http.StatusConflict, true
	case errors.Is(err, tableimport.ErrDiskFull):
		return http.StatusInsufficientStorage, true
	case errors.Is(err, errSheetsUnavailable):
		return http.StatusServiceUnavailable, true
	case errors.As(err, &fileErr), errors.As(err, &rowErrs), errors.Is(err, tableimport.ErrEmptyFile),
		errors.Is(err, tableimport.ErrTableMissing), errors.Is(err, tableimport.ErrColumnsMissing),
		errors.Is(err, tableimport.ErrTimedOut), errors.Is(err, tableimport.ErrSheetNotPublic):
		return http.StatusUnprocessableEntity, true
	}
	return http.StatusInternalServerError, false
}

func importErrorMessage(err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return tableimport.ErrFileTooLarge.Error()
	}
	if errors.Is(err, tableimport.ErrSheetDownload) {
		return tableimport.ErrSheetDownload.Error()
	}
	if _, known := importStatus(err); known {
		return err.Error()
	}
	return "the import failed"
}

func writeImportError(w http.ResponseWriter, err error) {
	status, known := importStatus(err)
	if !known {
		log.Printf("ERROR: table import: %v", err)
	}
	body := map[string]any{"error": importErrorMessage(err), "status": status}
	var rowErrs *tableimport.RowErrors
	if errors.As(err, &rowErrs) {
		body["rowErrors"] = rowErrs.Errors
	}
	writeJSONStatus(w, status, body)
}
