package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A column typed `timestamp` holds a bare clock time; read back, it is
// indistinguishable from a local one. Every moment this schema stores is a
// timestamptz.
var zonelessColumnType = regexp.MustCompile(`(?i)\b(?:timestamp|time)\b(?:\s*\(\d+\))?(?:\s+(?:without\s+time\s+zone|default|not\s+null|null)|\s*[,;)]|\s*$)`)

func TestNoMigrationDeclaresAZonelessTimestampColumn(t *testing.T) {
	files, err := filepath.Glob("migrations/*.up.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations not found: %v", err)
	}
	for _, file := range files {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for number, line := range strings.Split(string(source), "\n") {
			code, _, _ := strings.Cut(line, "--")
			if strings.Contains(strings.ToUpper(code), "INDEX") {
				continue
			}
			if zonelessColumnType.MatchString(code) && !strings.Contains(strings.ToLower(code), "timestamptz") {
				t.Errorf("%s:%d declares a zoneless time column: %s", file, number+1, strings.TrimSpace(line))
			}
		}
	}
}
