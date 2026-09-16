package scheduler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The bookkeeping tables live in the reserved `excalibase` schema, never in
// the tenant's `public` schema. An unqualified reference resolves through
// search_path and would silently recreate the table in user space, so every
// statement in this package must name the schema.
func TestSchedulerSQLIsSchemaQualified(t *testing.T) {
	unqualified := regexp.MustCompile(`(^|[^.\w])excalibase_(scheduled_functions|cron_jobs)\b`)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		body, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			// Comments and doc prose name the bare tables on purpose.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
				continue
			}
			if unqualified.MatchString(line) {
				t.Errorf("%s:%d references the bookkeeping table without the reserved schema: %s",
					name, i+1, trimmed)
			}
		}
	}
}
