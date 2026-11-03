package advisors

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func onlyCheck(findings []Finding, check string) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Check == check {
			out = append(out, f)
		}
	}
	return out
}

func TestAnonSelectWithoutFilterIsReported(t *testing.T) {
	doc := &domain.PermissionDocument{Tables: []domain.TablePermissions{
		{Table: "public.orders", Role: "anon", Select: raw(`{"filter":{},"columns":"*"}`)},
	}}
	got := onlyCheck(permissionFindings(doc), CheckAnonUnfiltered)
	if len(got) != 1 {
		t.Fatalf("findings = %+v", got)
	}
	f := got[0]
	if f.Category != CategorySecurity || f.Level != LevelWarning || f.Object != "public.orders" {
		t.Fatalf("finding = %+v", f)
	}
	if !strings.Contains(f.Message, "every row") || !strings.Contains(f.FixHint, "filter") {
		t.Fatalf("message %q hint %q", f.Message, f.FixHint)
	}
}

func TestAnonSelectWithFilterIsNotReported(t *testing.T) {
	doc := &domain.PermissionDocument{Tables: []domain.TablePermissions{
		{Table: "public.posts", Role: "anon", Select: raw(`{"filter":{"published":{"_eq":true}},"columns":"*"}`)},
		{Table: "public.notes", Role: "user", Select: raw(`{"filter":{},"columns":"*"}`)},
	}}
	if got := permissionFindings(doc); len(got) != 0 {
		t.Fatalf("findings = %+v", got)
	}
}

func TestTrivialFilters(t *testing.T) {
	for body, want := range map[string]bool{
		``:                              true,
		`null`:                          true,
		`{}`:                            true,
		`{"_and":[]}`:                   true,
		`{"_and":[{},{"_and":[]}]}`:     true,
		`{"_or":[{"a":{"_eq":1}},{}]}`:  true,
		`{"_or":[]}`:                    false,
		`{"a":{"_eq":1}}`:               false,
		`{"_and":[{},{"a":{"_eq":1}}]}`: false,
		`{"_not":{}}`:                   false,
	} {
		if got := trivial(raw(body)); got != want {
			t.Errorf("trivial(%s) = %v, want %v", body, got, want)
		}
	}
}

func TestAnonWritesWithoutFilter(t *testing.T) {
	doc := &domain.PermissionDocument{Tables: []domain.TablePermissions{{
		Table: "public.messages", Role: "anon",
		Select: raw(`{"filter":{},"columns":"*"}`),
		Insert: raw(`{"check":{},"columns":"*"}`),
		Update: raw(`{"filter":{},"check":{},"columns":"*"}`),
		Delete: raw(`{"filter":{}}`),
	}}}
	levels := map[string]string{}
	for _, f := range onlyCheck(permissionFindings(doc), CheckAnonUnfiltered) {
		levels[f.Operation] = f.Level
	}
	want := map[string]string{"select": LevelWarning, "insert": LevelInfo, "update": LevelError, "delete": LevelError}
	if len(levels) != len(want) {
		t.Fatalf("levels = %v", levels)
	}
	for op, level := range want {
		if levels[op] != level {
			t.Errorf("%s: level %q, want %q", op, levels[op], level)
		}
	}
}

// An update or delete reaches only rows the select filter lets the role read.
func TestAnonUpdateBoundedBySelectFilterIsNotReported(t *testing.T) {
	doc := &domain.PermissionDocument{Tables: []domain.TablePermissions{{
		Table: "public.drafts", Role: "anon",
		Select: raw(`{"filter":{"public":{"_eq":true}},"columns":"*"}`),
		Update: raw(`{"filter":{},"columns":"*"}`),
		Delete: raw(`{"filter":{}}`),
	}}}
	if got := onlyCheck(permissionFindings(doc), CheckAnonUnfiltered); len(got) != 0 {
		t.Fatalf("findings = %+v", got)
	}
}

func TestWriteWithoutSelect(t *testing.T) {
	doc := &domain.PermissionDocument{Tables: []domain.TablePermissions{
		{Table: "public.contact", Role: "anon", Insert: raw(`{"check":{"email":{"_is_null":false}},"columns":"*"}`)},
		{Table: "public.orders", Role: "editor", Insert: raw(`{"check":{}}`), Update: raw(`{"filter":{}}`)},
		{Table: "public.ok", Role: "user", Select: raw(`{"filter":{}}`), Update: raw(`{"filter":{}}`)},
	}}
	got := onlyCheck(permissionFindings(doc), CheckWriteWithoutSelect)
	if len(got) != 2 {
		t.Fatalf("findings = %+v", got)
	}
	byObject := map[string]Finding{}
	for _, f := range got {
		byObject[f.Object] = f
	}
	contact, orders := byObject["public.contact"], byObject["public.orders"]
	if contact.Level != LevelInfo || contact.Role != "anon" || !strings.Contains(contact.Message, "write-only") {
		t.Errorf("contact = %+v", contact)
	}
	if orders.Level != LevelWarning || orders.Role != "editor" || !strings.Contains(orders.Message, "update") {
		t.Errorf("orders = %+v", orders)
	}
}

func TestNilDocumentHasNoFindings(t *testing.T) {
	if got := permissionFindings(nil); len(got) != 0 {
		t.Fatalf("findings = %+v", got)
	}
}
