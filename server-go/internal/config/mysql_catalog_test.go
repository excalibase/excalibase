package config

import (
	"strings"
	"testing"
)

// ADR 0041: MySQL projects run 8.4 LTS only, from digest-pinned images.
func TestMySQLCatalog_OffersOnly84Pinned(t *testing.T) {
	versions := MySQLVersions()
	if len(versions) == 0 {
		t.Fatal("the MySQL catalogue lists no version")
	}
	for _, version := range versions {
		if !strings.HasPrefix(version, "8.4.") {
			t.Errorf("version %q is not 8.4 LTS", version)
		}
		image, err := MySQLImage(version)
		if err != nil {
			t.Fatalf("MySQLImage(%s): %v", version, err)
		}
		if !strings.Contains(image, "@sha256:") || !strings.Contains(image, ":"+version+"@") {
			t.Errorf("image %q for %s is not tagged and digest-pinned", image, version)
		}
	}
}

func TestMySQLCatalog_DefaultIsNewest(t *testing.T) {
	if got, want := DefaultMySQLVersion(), MySQLVersions()[0]; got != want {
		t.Errorf("default %q, want the newest %q", got, want)
	}
}

func TestMySQLImage_RefusesUnknownVersion(t *testing.T) {
	for _, version := range []string{"", "8.0.46", "8.4", "9.0.1", "8.4.999"} {
		if _, err := MySQLImage(version); err == nil {
			t.Errorf("MySQLImage(%q) accepted a version the catalogue does not list", version)
		}
	}
}

func TestParseMySQLCatalog_RefusesBadCatalogues(t *testing.T) {
	cases := map[string]string{
		"empty":        "versions: []\n",
		"unpinned":     "versions:\n  - version: \"8.4.10\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.10\n",
		"not 8.4":      "versions:\n  - version: \"8.0.46\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.0.46@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120\n",
		"tag mismatch": "versions:\n  - version: \"8.4.9\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120\n",
		"duplicate": "versions:\n  - version: \"8.4.10\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120\n" +
			"  - version: \"8.4.10\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120\n",
		"not newest first": "versions:\n  - version: \"8.4.9\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.9@sha256:f32deb585c9d16e1cd1bbc4768be1ea0fc33fd6798c6d9952bfd573ce9e5c792\n" +
			"  - version: \"8.4.10\"\n    image: ghcr.io/cybozu-go/moco/mysql:8.4.10@sha256:6a8b7362a5b15dd5343c69f316adfc5d1798924470b1d38ce9cad85febb23120\n",
	}
	for name, raw := range cases {
		if _, err := parseMySQLCatalog([]byte(raw)); err == nil {
			t.Errorf("%s: catalogue accepted", name)
		}
	}
}

// A minor upgrade only ever moves forward within the series.
func TestMySQLUpgradeAllowed(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{"8.4.9", "8.4.10", true},
		{"8.4.10", "8.4.10", false},
		{"8.4.10", "8.4.9", false},
		{"8.4.9", "8.0.46", false},
	}
	for _, c := range cases {
		err := MySQLUpgradeAllowed(c.from, c.to)
		if (err == nil) != c.ok {
			t.Errorf("MySQLUpgradeAllowed(%s, %s) = %v, want ok=%v", c.from, c.to, err, c.ok)
		}
	}
}

// 512Mi left mysqld no headroom: it was OOM-killed restarting onto a new
// version (EXC-583, k3d). MySQL runs on at least 1Gi; larger plans keep theirs.
func TestMySQLPlanRaisesMemoryToTheFloor(t *testing.T) {
	free, err := MySQLPlan(TierConfig{Instances: 1, Memory: "512Mi", CPU: "0.5"})
	if err != nil || free.Memory != "1Gi" || free.CPU != "0.5" || free.Instances != 1 {
		t.Errorf("free: %+v %v", free, err)
	}
	standard, err := MySQLPlan(TierConfig{Instances: 3, Memory: "4Gi"})
	if err != nil || standard.Memory != "4Gi" {
		t.Errorf("standard: %+v %v", standard, err)
	}
	if _, err := MySQLPlan(TierConfig{Memory: "lots"}); err == nil {
		t.Error("an unreadable memory was accepted")
	}
}
