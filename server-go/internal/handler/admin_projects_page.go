package handler

import (
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// projectSorts is every column the project list may be sorted by. A sort name
// from the request is only ever looked up here, never put into a query.
var projectSorts = map[string]func(a, b projectEntry) int{
	"name": func(a, b projectEntry) int {
		return strings.Compare(strings.ToLower(a.name()), strings.ToLower(b.name()))
	},
	"org": func(a, b projectEntry) int {
		return strings.Compare(strings.ToLower(a.fact.OrgName), strings.ToLower(b.fact.OrgName))
	},
	"owner": func(a, b projectEntry) int {
		return strings.Compare(strings.ToLower(a.fact.OwnerEmail), strings.ToLower(b.fact.OwnerEmail))
	},
	"plan":    func(a, b projectEntry) int { return strings.Compare(string(a.inst.Tier), string(b.inst.Tier)) },
	"status":  func(a, b projectEntry) int { return strings.Compare(string(a.inst.Status), string(b.inst.Status)) },
	"created": func(a, b projectEntry) int { return a.created().Compare(b.created()) },
}

// projectStatuses and projectPlans are the values the list may be filtered by.
var projectStatuses = map[string]bool{
	string(domain.StatusActive): true, string(domain.StatusPaused): true, string(domain.StatusPausing): true,
	string(domain.StatusResuming): true, string(domain.StageFailed): true, domain.StatusProvisioning: true,
	string(domain.StatusDeleting): true, string(domain.StatusBackupsPendingDelete): true,
	string(domain.StatusRestoring): true, string(domain.StatusPendingDeletion): true, string(domain.StatusUpgrading): true,
}

var projectPlans = map[string]bool{
	string(domain.Free): true, string(domain.Standard): true, string(domain.Enterprise): true,
}

type projectEntry struct {
	inst *domain.DatabaseInstance
	fact adminconsole.ProjectFact
}

func (e projectEntry) name() string {
	if e.inst.ProjectName != "" {
		return e.inst.ProjectName
	}
	return e.inst.ProjectID
}

func (e projectEntry) created() time.Time {
	if e.inst.CreatedAt == nil {
		return time.Time{}
	}
	return e.inst.CreatedAt.Time
}

// projectQuery is what a paged project list asks for.
type projectQuery struct {
	search, status, plan, owner, flag, sortBy string
	desc                                      bool
	limit, offset                             int
}

var errBadProjectQuery = errors.New("invalid project list query")

// parseProjectQuery reads and validates the list parameters. The list is paged
// only when `limit` is present, so a caller that sends none still gets the
// whole list as a bare array.
func parseProjectQuery(rawQuery string) (q projectQuery, paged bool, err error) {
	// Query() would silently drop a parameter it cannot parse; on the paged path a
	// malformed one is refused, on the unpaged path it is ignored as it always was.
	values, parseErr := url.ParseQuery(rawQuery)
	if !values.Has("limit") {
		return q, false, nil
	}
	if parseErr != nil {
		return q, true, errBadProjectQuery
	}
	if q.limit, err = positiveInt(values.Get("limit")); err != nil {
		return q, true, err
	}
	q.limit = min(q.limit, adminconsole.MaxPageSize)
	if values.Has("offset") {
		if q.offset, err = strconv.Atoi(values.Get("offset")); err != nil || q.offset < 0 {
			return q, true, errBadProjectQuery
		}
	}
	q.sortBy, q.desc = "created", true
	if values.Has("sort") {
		q.sortBy = values.Get("sort")
		q.desc = false
	}
	if _, known := projectSorts[q.sortBy]; !known {
		return q, true, errBadProjectQuery
	}
	switch values.Get("order") {
	case "":
	case "asc":
		q.desc = false
	case "desc":
		q.desc = true
	default:
		return q, true, errBadProjectQuery
	}
	q.search = strings.ToLower(strings.TrimSpace(values.Get("q")))
	q.status, q.plan = values.Get("status"), strings.ToUpper(values.Get("plan"))
	q.owner, q.flag = values.Get("owner"), values.Get("flag")
	if !projectStatuses[q.status] && q.status != "" || !projectPlans[q.plan] && q.plan != "" {
		return q, true, errBadProjectQuery
	}
	if (q.owner != "" && q.owner != "active" && q.owner != "suspended") || (q.flag != "" && q.flag != "protected") {
		return q, true, errBadProjectQuery
	}
	return q, true, nil
}

func positiveInt(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, errBadProjectQuery
	}
	return n, nil
}

func (q projectQuery) matches(e projectEntry) bool {
	if q.status != "" && string(e.inst.Status) != q.status {
		return false
	}
	if q.plan != "" && strings.ToUpper(string(e.inst.Tier)) != q.plan {
		return false
	}
	if q.owner != "" && (e.fact.OwnerStatus == adminconsole.StatusSuspended) != (q.owner == "suspended") {
		return false
	}
	if q.flag == "protected" && (e.inst.DeletionProtection == nil || !*e.inst.DeletionProtection) {
		return false
	}
	if q.search == "" {
		return true
	}
	for _, field := range []string{e.inst.ProjectName, e.inst.ProjectID, e.fact.OrgName, e.fact.OwnerEmail} {
		if strings.Contains(strings.ToLower(field), q.search) {
			return true
		}
	}
	return false
}

// page filters, orders and slices the entries; total counts what the filters match.
func (q projectQuery) page(entries []projectEntry) (shown []projectEntry, total int) {
	matched := make([]projectEntry, 0, len(entries))
	for _, e := range entries {
		if q.matches(e) {
			matched = append(matched, e)
		}
	}
	compare := projectSorts[q.sortBy]
	sort.SliceStable(matched, func(i, j int) bool {
		c := compare(matched[i], matched[j])
		if c == 0 {
			c = strings.Compare(matched[i].inst.ProjectID, matched[j].inst.ProjectID)
		}
		if q.desc {
			return c > 0
		}
		return c < 0
	})
	total = len(matched)
	from := min(q.offset, total)
	return matched[from:min(from+q.limit, total)], total
}

func writeBadProjectQuery(w http.ResponseWriter) {
	httpError(w, "invalid list parameters: limit must be a positive number, offset zero or more, sort one of name, org, owner, plan, status, created, order asc or desc", http.StatusBadRequest)
}
