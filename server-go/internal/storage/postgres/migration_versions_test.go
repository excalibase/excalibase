package postgres

import (
	"regexp"
	"sort"
	"strconv"
	"testing"
)

// golang-migrate's Up() never applies a version below the database's current
// one, so a migration merged under a lower number than one already shipped is
// silently skipped. Versions must run on from the highest with no new gap.
var migrationFile = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.(up|down)\.sql$`)

// Versions skipped before this check existed; nothing may be added to this list.
var historicalMigrationGaps = map[int]bool{5: true, 21: true, 22: true, 35: true, 36: true, 37: true, 38: true, 39: true}

func TestMigrationVersionsAreContiguous(t *testing.T) {
	versions := migrationVersions(t)
	if len(versions) == 0 || versions[0] != 1 {
		t.Fatalf("migrations must start at version 1, got %v", versions)
	}
	for i := 1; i < len(versions); i++ {
		for missing := versions[i-1] + 1; missing < versions[i]; missing++ {
			if !historicalMigrationGaps[missing] {
				t.Errorf("version %d is missing between %d and %d; number a new migration after the highest", missing, versions[i-1], versions[i])
			}
		}
	}
}

// migrationVersions returns the sorted versions, failing on misnamed files,
// a direction given twice, or a version without both up and down.
func migrationVersions(t *testing.T) []int {
	t.Helper()
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	directions := map[int]map[string]string{}
	for _, entry := range entries {
		match := migrationFile.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Errorf("%s is not named <version>_<name>.(up|down).sql", entry.Name())
			continue
		}
		version, _ := strconv.Atoi(match[1])
		addMigrationFile(t, directions, version, match[2], entry.Name())
	}
	versions := make([]int, 0, len(directions))
	for version, files := range directions {
		if len(files) != 2 {
			t.Errorf("version %d needs both an up and a down migration, has %v", version, files)
		}
		versions = append(versions, version)
	}
	sort.Ints(versions)
	return versions
}

func addMigrationFile(t *testing.T, directions map[int]map[string]string, version int, direction, name string) {
	t.Helper()
	if directions[version] == nil {
		directions[version] = map[string]string{}
	}
	if previous, taken := directions[version][direction]; taken {
		t.Errorf("version %d has two %s migrations: %s and %s", version, direction, previous, name)
	}
	directions[version][direction] = name
}
