package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// hostLoki answers query_range with lines and records what was asked.
type hostLoki struct {
	query  string
	start  int64
	end    int64
	limit  string
	status int
	lines  []string
}

func (l *hostLoki) server(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("path %s", r.URL.Path)
		}
		l.query = r.URL.Query().Get("query")
		l.start, _ = strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		l.end, _ = strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		l.limit = r.URL.Query().Get("limit")
		if l.status != 0 {
			w.WriteHeader(l.status)
			return
		}
		values := make([]string, 0, len(l.lines))
		for i, line := range l.lines {
			values = append(values, `["`+strconv.Itoa(1000+i)+`",`+strconv.Quote(line)+`]`)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{},"values":[` + strings.Join(values, ",") + `]}]}}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// EXC-672: with stored logs on, a single host's search reads Loki with the
// same query code as Kubernetes, selecting only the project's database.
func TestHostLogSearchReadsLokiForTheProjectOnly(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	loki := &hostLoki{lines: []string{"ERROR:  relation \"x\" does not exist"}}
	svc.SetHostLogStore(loki.server(t).URL)
	engine := &engineLogs{out: "never read\n"}
	svc.SetSingleHostLogs(engine)

	page, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 50, Term: "Relation\"} or {x=\"", Since: 6 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if page.Lines != "ERROR:  relation \"x\" does not exist\n" {
		t.Errorf("lines = %q", page.Lines)
	}
	want := `{project_id="host-a",component="database"} |~ "(?i)Relation\"\\} or \\{x=\""`
	if loki.query != want {
		t.Errorf("query = %s\nwant    %s", loki.query, want)
	}
	if loki.limit != "50" || time.Duration(loki.end-loki.start) != 6*time.Hour {
		t.Errorf("limit %s window %v", loki.limit, time.Duration(loki.end-loki.start))
	}
	if engine.container != "" {
		t.Errorf("the engine log was read although Loki answered")
	}
}

func TestHostLogSearchClampsToThePlansRetention(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	loki := &hostLoki{lines: []string{"LOG: ready"}}
	svc.SetHostLogStore(loki.server(t).URL)
	svc.SetLogRetention(features.NewStatic(features.LogRetention),
		config.LogRetention{Default: 168 * time.Hour, ByPlan: map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour}}, nil)

	page, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10, Since: 7 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if page.KeptFor != 24*time.Hour || time.Duration(loki.end-loki.start) != 24*time.Hour {
		t.Errorf("kept %v, window %v; want the free plan's day", page.KeptFor, time.Duration(loki.end-loki.start))
	}
}

// As the pod log on Kubernetes: no stored lines yet, or Loki down, reads the engine.
func TestHostLogSearchFallsBackToTheEngineLog(t *testing.T) {
	for name, loki := range map[string]*hostLoki{"empty": {}, "down": {status: http.StatusBadGateway}} {
		t.Run(name, func(t *testing.T) {
			svc := singleHostLogService(t, domain.PostgreSQL)
			svc.SetHostLogStore(loki.server(t).URL)
			engine := &engineLogs{out: "LOG: ready\n"}
			svc.SetSingleHostLogs(engine)
			page, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10})
			if err != nil || page.Lines != "LOG: ready\n" || engine.container != "c0ffee" {
				t.Errorf("page %q err %v engine read %q", page.Lines, err, engine.container)
			}
		})
	}
}

func TestHostLogSearchFailsClosedWhenBothAreDown(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	svc.SetHostLogStore((&hostLoki{status: http.StatusInternalServerError}).server(t).URL)
	svc.SetSingleHostLogs(&engineLogs{err: errors.New("engine proxy: 502")})
	if _, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10}); !errors.Is(err, ErrLogsUnavailable) {
		t.Errorf("err = %v", err)
	}
}

// Loki answering with nothing while the engine is down is an empty page, as on Kubernetes.
func TestHostLogSearchEmptyLokiAndEngineDownIsEmpty(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	svc.SetHostLogStore((&hostLoki{}).server(t).URL)
	svc.SetSingleHostLogs(&engineLogs{err: errors.New("no such container")})
	page, err := svc.SearchProjectLogs(context.Background(), "host-a", LogSearch{Lines: 10})
	if err != nil || page.Lines != "" {
		t.Errorf("page %q err %v", page.Lines, err)
	}
}

// A project id that could not have been generated never reaches a selector.
func TestHostLogSelectorRefusesAnUnsafeProjectID(t *testing.T) {
	if _, ok := hostLogSelector("proj-abc123"); !ok {
		t.Error("a generated id is refused")
	}
	for _, id := range []string{"", "A", "proj\"}", "proj,x=\"y", strings.Repeat("a", 64), "-proj"} {
		if selector, ok := hostLogSelector(id); ok {
			t.Errorf("%q gave selector %s", id, selector)
		}
	}
}

func TestHostLogSearchRefusesAnUnsafeProjectIDStoredOnTheInstance(t *testing.T) {
	svc := singleHostLogService(t, domain.PostgreSQL)
	inst, _ := svc.GetInstance("host-a")
	inst.ProjectID = "Bad\"id"
	loki := &hostLoki{lines: []string{"x"}}
	svc.SetHostLogStore(loki.server(t).URL)
	svc.SetSingleHostLogs(&engineLogs{out: "engine\n"})
	page, err := svc.searchSingleHostLogs(context.Background(), inst, LogSearch{Lines: 10})
	if err != nil || loki.query != "" || page.Lines != "engine\n" {
		t.Errorf("page %q err %v loki asked %q", page.Lines, err, loki.query)
	}
}
