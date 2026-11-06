package imagepull

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A test that starts a container straight from testcontainers pulls its image
// through a path that drops a pull failure, and the run fails later as "No such
// image". Every such test file must pre-pull through this package.
func TestEveryContainerStartingTestFilePrePulls(t *testing.T) {
	starters := []string{"testcontainers.GenericContainer(", "postgres.Run(", "mysql.Run("}
	var bare []string
	err := filepath.WalkDir("..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		for _, starter := range starters {
			if strings.Contains(text, starter) && !strings.Contains(text, "imagepull.EnsureLocal(") {
				bare = append(bare, path)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) > 0 {
		t.Fatalf("these test files start containers without imagepull.EnsureLocal: %v", bare)
	}
}
