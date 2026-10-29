package provisioner

import (
	"archive/tar"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func tarOf(t *testing.T, names ...string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, name := range names {
		typeflag := byte(tar.TypeReg)
		if strings.HasSuffix(name, "/") {
			typeflag = tar.TypeDir
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: typeflag, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
	}
	_ = writer.Close()
	return bytes.NewReader(buf.Bytes())
}

func TestExtensionNamesAreTheControlFiles(t *testing.T) {
	names, err := ExtensionNames(tarOf(t, "extension/", "extension/vector.control", "extension/vector--0.8.sql", "extension/citext.control", "extension/sub/x.control/"))
	if err != nil || strings.Join(names, ",") != "citext,vector" {
		t.Fatalf("names = %v, %v", names, err)
	}
	if _, err := ExtensionNames(tarOf(t, "extension/")); !errors.Is(err, ErrNoExtensions) {
		t.Fatalf("an empty directory: %v", err)
	}
	if _, err := ExtensionNames(strings.NewReader("not a tar")); err == nil {
		t.Fatal("garbage was read as a list")
	}
}

func TestPreUpgradeContainerName(t *testing.T) {
	if got := PreUpgradeContainerName("p1", "mu-1"); got != "excalibase-p1-postgres-pre-upgrade-mu-1" {
		t.Fatalf("name = %q", got)
	}
}

// The new server takes no network connection until the platform lifts the
// fence, and never one as the superuser; the load tolerates one error only.
func TestUpgradeInitScript(t *testing.T) {
	script := upgradeInitScript()
	fence := strings.Index(script, upgradeFenceLine)
	reject := strings.Index(script, superuserNetworkReject)
	load := strings.Index(script, "-f "+UpgradeGlobalsFile)
	if fence < 0 || reject < 0 || load < 0 || fence > load || reject > load {
		t.Fatalf("fence %d reject %d load %d", fence, reject, load)
	}
	for _, want := range []string{"trap ", `role "postgres" already exists`, "archive_mode = 'on'", "/walarchive/%f", UpgradeLoaded, UpgradeFailed} {
		if !strings.Contains(script, want) {
			t.Errorf("init script misses %q", want)
		}
	}
	archive, err := UpgradeInitTar()
	if err != nil {
		t.Fatal(err)
	}
	header, err := tar.NewReader(bytes.NewReader(archive)).Next()
	if err != nil || header.Name != "excalibase-major-upgrade.sh" || header.Mode != 0o755 {
		t.Fatalf("init tar: %+v, %v", header, err)
	}
}

func TestFenceAndUnfence(t *testing.T) {
	if !strings.Contains(FenceScript(), upgradeFenceLine) || !strings.Contains(FenceScript(), "pg_terminate_backend") {
		t.Fatal("the fence neither refuses nor ends connections")
	}
	if !strings.Contains(UnfenceScript(), "grep -vxF '"+upgradeFenceLine+"'") {
		t.Fatal("the unfence does not remove the fence line")
	}
	if !strings.Contains(upgradeInitScript(), "--section=data --use-set-session-authorization") || !strings.Contains(CleanupScript(), UpgradeDir) {
		t.Fatal("dump or cleanup scripts")
	}
}
