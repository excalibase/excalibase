package advisors

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const anonRole = "anon"

// ownerFilterExample is the shape of a filter that limits rows to their owner.
const ownerFilterExample = `{"owner_id":{"_eq":"X-Excalibase-User-Id"}}`

// permissionFindings reads the project's API permissions (ADR 0036). A table
// with no permission is unreachable, so the risk is a permission that reaches
// every row for anyone, or one the engine drops.
func permissionFindings(doc *domain.PermissionDocument) []Finding {
	if doc == nil {
		return nil
	}
	var findings []Finding
	for _, entry := range doc.Tables {
		if entry.Role == anonRole {
			findings = append(findings, anonFindings(entry)...)
		}
		if f, ok := writeWithoutSelect(entry); ok {
			findings = append(findings, f)
		}
	}
	return findings
}

func anonFindings(entry domain.TablePermissions) []Finding {
	var findings []Finding
	selectOpen := entry.Select != nil && trivial(field(entry.Select, "filter"))
	if selectOpen {
		findings = append(findings, anonFinding(entry.Table, domain.PermissionSelect, LevelWarning,
			"Anyone, without signing in, can read every row of "+entry.Table+": anon's select permission has no row filter.",
			"Expected for public data such as a product catalogue. Otherwise give anon's select permission a filter, "+
				"or remove it and grant select to the user role with a filter such as "+ownerFilterExample+"."))
	}
	if entry.Insert != nil && trivial(field(entry.Insert, "check")) {
		findings = append(findings, anonFinding(entry.Table, domain.PermissionInsert, LevelInfo,
			"Anyone, without signing in, can add rows to "+entry.Table+": anon's insert permission has no check.",
			"Expected for a public form. Otherwise add a check that new rows must pass, or a column preset, to anon's insert permission."))
	}
	// An update or delete reaches only rows the select filter also reaches.
	for _, write := range []struct {
		op         string
		definition json.RawMessage
		verb       string
	}{
		{domain.PermissionUpdate, entry.Update, "change"},
		{domain.PermissionDelete, entry.Delete, "delete"},
	} {
		if write.definition != nil && selectOpen && trivial(field(write.definition, "filter")) {
			findings = append(findings, anonFinding(entry.Table, write.op, LevelError,
				fmt.Sprintf("Anyone, without signing in, can %s every row of %s: anon's %s and select permissions have no row filter.",
					write.verb, entry.Table, write.op),
				fmt.Sprintf("Remove anon's %s permission, or grant it to the user role with a filter such as %s.", write.op, ownerFilterExample)))
		}
	}
	return findings
}

func anonFinding(table, op, level, message, hint string) Finding {
	return Finding{
		Check: CheckAnonUnfiltered, Category: CategorySecurity, Level: level, Object: table,
		Role: anonRole, Operation: op, Message: message, FixHint: hint,
	}
}

// writeWithoutSelect reports a role that may write a table it cannot read.
// Update and delete need select, so the engine drops them; insert alone is a
// write-only table, which a contact form wants and most tables do not.
func writeWithoutSelect(entry domain.TablePermissions) (Finding, bool) {
	if entry.Select != nil {
		return Finding{}, false
	}
	var dropped []string
	if entry.Update != nil {
		dropped = append(dropped, domain.PermissionUpdate)
	}
	if entry.Delete != nil {
		dropped = append(dropped, domain.PermissionDelete)
	}
	f := Finding{Check: CheckWriteWithoutSelect, Category: CategorySecurity, Object: entry.Table, Role: entry.Role}
	switch {
	case len(dropped) > 0:
		ops := strings.Join(dropped, " and ")
		f.Level, f.Operation = LevelWarning, strings.Join(dropped, ",")
		f.Message = fmt.Sprintf("%s has %s permission on %s but no select permission, so the engine ignores it: %s cannot %s any row.",
			entry.Role, ops, entry.Table, entry.Role, ops)
		f.FixHint = fmt.Sprintf("Add a select permission for %s on %s (its filter also limits which rows the %s reaches), or remove the %s permission.",
			entry.Role, entry.Table, ops, ops)
		return f, true
	case entry.Insert != nil:
		f.Level, f.Operation = LevelInfo, domain.PermissionInsert
		f.Message = fmt.Sprintf("%s may insert into %s but not read it: the table is write-only for %s, and an insert answers a count, never the row.",
			entry.Role, entry.Table, entry.Role)
		f.FixHint = fmt.Sprintf("Expected for a form. If %s should read back what it writes, add a select permission whose filter matches those rows.", entry.Role)
		return f, true
	}
	return Finding{}, false
}

// field is one key of a permission definition; nil when absent.
func field(definition json.RawMessage, key string) json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(definition, &object) != nil {
		return nil
	}
	return object[key]
}

// trivial reports a boolean expression that is true for every row: absent,
// {}, an _and of trivial terms, or an _or with one.
func trivial(expression json.RawMessage) bool {
	trimmed := bytes.TrimSpace(expression)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(trimmed, &object) != nil {
		return false
	}
	if len(object) == 0 {
		return true
	}
	if len(object) != 1 {
		return false
	}
	for key, value := range object {
		var terms []json.RawMessage
		if (key != "_and" && key != "_or") || json.Unmarshal(value, &terms) != nil {
			return false
		}
		return termsTrivial(key, terms)
	}
	return false
}

func termsTrivial(op string, terms []json.RawMessage) bool {
	if op == "_and" {
		for _, term := range terms {
			if !trivial(term) {
				return false
			}
		}
		return true
	}
	for _, term := range terms {
		if trivial(term) {
			return true
		}
	}
	return false
}
