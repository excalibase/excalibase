package provisioner

import (
	"os"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
)

// The catalogue ships with no published image digests until the image publish
// workflow has run and the digests have been recorded. Provisioning refuses an
// unpublished major on purpose; these suites are about everything downstream
// of that, so they run against a catalogue whose majors are all published.
// The refusals themselves are covered in internal/config.
func TestMain(m *testing.M) {
	restore := config.PublishPostgresCatalogForTest()
	code := m.Run()
	restore()
	os.Exit(code)
}
