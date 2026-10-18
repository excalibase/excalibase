package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A search term is a literal, case-insensitive line filter: quotes, braces and
// regex metacharacters stay inside the LogQL string and match themselves.
func TestLogSearchTermIsALiteralCaseInsensitiveLineFilter(t *testing.T) {
	for _, term := range []string{
		"relation does not exist",
		`a"b\c`,
		`"} or {namespace=~".+`,
		".*",
		"(unclosed[",
		"`backtick`",
	} {
		t.Run(term, func(t *testing.T) {
			logql := projectLogQL("org1-proj-a", "proj-a", term)
			selector := `{namespace="org1-proj-a",cnpg_io_cluster="proj-a-postgres"}`
			if !strings.HasPrefix(logql, selector+" |~ ") {
				t.Fatalf("logql = %s, want the project selector then one line filter", logql)
			}
			pattern, err := strconv.Unquote(strings.TrimPrefix(logql, selector+" |~ "))
			if err != nil {
				t.Fatalf("the filter is not one quoted string: %v (%s)", err, logql)
			}
			filter, err := regexp.Compile(pattern)
			if err != nil {
				t.Fatalf("filter %q does not compile: %v", pattern, err)
			}
			if !filter.MatchString("before " + strings.ToUpper(term) + " after") {
				t.Fatalf("filter %q misses a line containing the term", pattern)
			}
			if filter.MatchString("a line without it") {
				t.Fatalf("filter %q matches a line without the term", pattern)
			}
		})
	}
	if got := projectLogQL("org1-proj-a", "proj-a", ""); got != `{namespace="org1-proj-a",cnpg_io_cluster="proj-a-postgres"}` {
		t.Fatalf("no term: %s", got)
	}
}

func TestLogSearchRefusesUnusableTerms(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	for name, term := range map[string]string{
		"too long":     strings.Repeat("x", maxLogSearchTerm+1),
		"control char": "a\nb",
		"not utf-8":    "\xff",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 10, Term: term})
			if !errors.Is(err, ErrInvalidLogSearch) {
				t.Fatalf("err = %v, want ErrInvalidLogSearch", err)
			}
		})
	}
}

// fakeLoki stores lines per namespace and answers query_range the way Loki
// would for the selector and line filter provisioning sends.
type fakeLoki struct {
	lines   map[string][]string
	queries []string
	starts  []int64
}

var selectorPattern = regexp.MustCompile(`^\{namespace="([^"]+)",cnpg_io_cluster="([^"]+)"\}(?: \|~ (".*"))?$`)

func (f *fakeLoki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	f.queries = append(f.queries, query)
	start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	f.starts = append(f.starts, start)
	match := selectorPattern.FindStringSubmatch(query)
	if match == nil {
		http.Error(w, "unexpected query", http.StatusBadRequest)
		return
	}
	filter := regexp.MustCompile("")
	if match[3] != "" {
		pattern, _ := strconv.Unquote(match[3])
		filter = regexp.MustCompile(pattern)
	}
	var values [][]string
	for _, line := range f.lines[match[1]] {
		if filter.MatchString(line) {
			values = append(values, []string{"1", line})
		}
	}
	result := []map[string]any{{"values": values}}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": result}})
}

// A project's search reads its own namespace only: project B's lines never
// reach project A, whatever A searches for.
func TestLogSearchReadsOnlyTheProjectsOwnLogs(t *testing.T) {
	svc, store, _ := setupOpsTest(t)
	port := 5432
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "proj-b", OrgID: "org2", Namespace: "org2-proj-b",
		DBType: domain.PostgreSQL, Tier: domain.Free, Status: "ACTIVE", PostgresVersion: "17",
		Host: "h.local", Port: &port, DatabaseName: "app", Username: "app", SSLMode: "require",
	}); err != nil {
		t.Fatal(err)
	}
	loki := &fakeLoki{lines: map[string][]string{
		testOpsDBNS:   {"ERROR: secret-marker in project A"},
		"org2-proj-b": {"ERROR: secret-marker in project B"},
	}}
	server := httptest.NewServer(loki)
	defer server.Close()
	svc.SetLokiURL(server.URL)

	logs, err := svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 100, Term: "SECRET-MARKER"})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if !strings.Contains(logs, "project A") || strings.Contains(logs, "project B") {
		t.Fatalf("project A's search returned %q", logs)
	}
	want := `{namespace="` + testOpsDBNS + `",cnpg_io_cluster="` + testOpsDB + `-postgres"}`
	if len(loki.queries) != 1 || !strings.HasPrefix(loki.queries[0], want+" |~ ") {
		t.Fatalf("queries = %q, want A's selector", loki.queries)
	}
}

func TestLogSearchLooksBackAsFarAsAsked(t *testing.T) {
	svc, _, _ := setupOpsTest(t)
	loki := &fakeLoki{lines: map[string][]string{testOpsDBNS: {"one"}}}
	server := httptest.NewServer(loki)
	defer server.Close()
	svc.SetLokiURL(server.URL)

	for since, want := range map[time.Duration]time.Duration{0: time.Hour, 7 * 24 * time.Hour: 7 * 24 * time.Hour} {
		before := time.Now()
		if _, err := svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 10, Since: since}); err != nil {
			t.Fatal(err)
		}
		start := time.Unix(0, loki.starts[len(loki.starts)-1])
		if got := before.Sub(start); got < want-time.Minute || got > want+time.Minute {
			t.Fatalf("since %v: searched back %v, want %v", since, got, want)
		}
	}
}

// Without Loki the term filters the live pod log, as Studio used to do itself.
func TestLogSearchWithoutLokiFiltersThePodLog(t *testing.T) {
	svc, _, mock := setupOpsTest(t)
	mock.PodLogs[testOpsDBNS+"/ops-db-postgres-1"] = "checkpoint starting\nERROR: Relation X\nconnection received\n"

	logs, err := svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 50, Term: "relation x"})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if logs != "ERROR: Relation X\n" {
		t.Fatalf("logs = %q", logs)
	}
	assertNoExecIntoThePod(t, mock)
}

// Loki holds nothing yet for a pod the collector has not shipped: the live pod
// log answers instead of an empty page.
func TestLogSearchFallsBackToThePodLogWhenLokiHasNoLines(t *testing.T) {
	svc, _, mock := setupOpsTest(t)
	mock.PodLogs[testOpsDBNS+"/ops-db-postgres-1"] = "database system is ready\n"
	server := httptest.NewServer(&fakeLoki{lines: map[string][]string{}})
	defer server.Close()
	svc.SetLokiURL(server.URL)

	logs, err := svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 10})
	if err != nil || logs != "database system is ready\n" {
		t.Fatalf("logs = %q, err = %v", logs, err)
	}

	// Loki answered: a pod that cannot be read is no outage, just no lines.
	delete(mock.PodLogs, testOpsDBNS+"/ops-db-postgres-1")
	logs, err = svc.SearchLogs(context.Background(), testOpsDB, LogSearch{Lines: 10})
	if err != nil || logs != "" {
		t.Fatalf("logs = %q, err = %v", logs, err)
	}
}
