package config

import (
	"strings"
	"testing"
)

func TestPostgresMajorsAreTheFourSupportedOnes(t *testing.T) {
	got := PostgresMajors()
	want := []string{"14", "15", "16", "17"}
	if len(got) != len(want) {
		t.Fatalf("majors: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("majors: got %v, want %v", got, want)
		}
	}
}

func TestPostgresMajorsIsACopy(t *testing.T) {
	first := PostgresMajors()
	first[0] = "mutated"
	if PostgresMajors()[0] != "14" {
		t.Fatal("PostgresMajors handed out the backing array")
	}
}

func TestLookupPostgresMajorRejectsUnknown(t *testing.T) {
	// 18 is refused exactly like 13: it is not in the catalogue, so nothing
	// downstream can be asked to provision it.
	for _, major := range []string{"13", "18", "19", "17.2", "", "  ", "latest"} {
		if _, ok := LookupPostgresMajor(major); ok {
			t.Errorf("LookupPostgresMajor(%q) accepted an unsupported major", major)
		}
	}
}

func TestLookupPostgresMajorTrimsWhitespace(t *testing.T) {
	entry, ok := LookupPostgresMajor(" 17 ")
	if !ok {
		t.Fatal("LookupPostgresMajor did not trim surrounding whitespace")
	}
	if entry.Major != "17" {
		t.Fatalf("Major: got %q, want %q", entry.Major, "17")
	}
}

func TestDocumentDBSupportedMatchesWhatTheImagesWereProvenToDo(t *testing.T) {
	// We compile DocumentDB ourselves, so the supported set is the set whose
	// image built and passed the collection/insert/read smoke test: 15, 16 and
	// 17. 14 does not compile (MarkGUCPrefixReserved arrived in 15) and must
	// report false so a project asking for DocumentDB there is refused before
	// anything is provisioned.
	tests := map[string]bool{"14": false, "15": true, "16": true, "17": true}
	for major, want := range tests {
		if got := DocumentDBSupported(major); got != want {
			t.Errorf("DocumentDBSupported(%q): got %v, want %v", major, got, want)
		}
	}
}

func TestDocumentDBSupportedIsFalseForUnknownMajor(t *testing.T) {
	if DocumentDBSupported("13") {
		t.Fatal("an unsupported major must not report DocumentDB support")
	}
}

func TestEveryBaseImageIsPinnedByDigest(t *testing.T) {
	for _, entry := range postgresCatalog.Majors {
		if !strings.Contains(entry.BaseImage, "@sha256:") {
			t.Errorf("major %s: baseImage %q is not pinned by digest", entry.Major, entry.BaseImage)
		}
	}
}

func TestPostgresImageRefusesAnUnsupportedMajor(t *testing.T) {
	_, err := PostgresImage("13")
	if err == nil {
		t.Fatal("PostgresImage accepted an unsupported major")
	}
	if !strings.Contains(err.Error(), "13") {
		t.Errorf("error should name the rejected major, got %q", err)
	}
}

// An unpublished major must fail loudly rather than resolving to a tag or to
// some other major's image.
func TestPostgresImageRefusesAnUnpublishedMajor(t *testing.T) {
	entry, ok := LookupPostgresMajor("17")
	if !ok {
		t.Fatal("17 missing from the catalogue")
	}
	if entry.Image != "" {
		t.Skip("17 has a published image; nothing to assert about the unpublished path")
	}
	if _, err := PostgresImage("17"); err == nil {
		t.Fatal("PostgresImage returned an image for a major with no published digest")
	}
}

func TestParseCatalogRejectsADuplicateMajor(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: "17"
    baseImage: ghcr.io/x/y@sha256:aa
  - major: "17"
    baseImage: ghcr.io/x/y@sha256:bb
`))
	if err == nil {
		t.Fatal("a duplicate major must be fatal")
	}
}

func TestParseCatalogRejectsAFloatingBaseImage(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: "17"
    baseImage: ghcr.io/cloudnative-pg/postgresql:17
`))
	if err == nil {
		t.Fatal("a tag-referenced baseImage must be fatal")
	}
}

func TestParseCatalogRejectsAFloatingImage(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: "17"
    baseImage: ghcr.io/x/y@sha256:aa
    image: ghcr.io/excalibase/postgresql:17
`))
	if err == nil {
		t.Fatal("a tag-referenced image must be fatal")
	}
}

func TestParseCatalogRejectsAnEmptyMajor(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: ""
    baseImage: ghcr.io/x/y@sha256:aa
`))
	if err == nil {
		t.Fatal("an empty major must be fatal")
	}
}

func TestParseCatalogRejectsAnEmptyCatalog(t *testing.T) {
	if _, err := parsePostgresCatalog([]byte("majors: []\n")); err == nil {
		t.Fatal("a catalogue with no majors must be fatal")
	}
}

func TestParseCatalogRejectsUnparseableYAML(t *testing.T) {
	if _, err := parsePostgresCatalog([]byte("majors: [oops\n")); err == nil {
		t.Fatal("unparseable YAML must be fatal")
	}
}

func TestParseCatalogRejectsAnUnknownField(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: "17"
    baseImage: ghcr.io/x/y@sha256:aa
    dokumentdb: true
`))
	if err == nil {
		t.Fatal("a misspelled field must be fatal, not silently ignored")
	}
}

// We own which DocumentDB every project runs, so the tag has to be recorded
// and has to be a tag — a branch or a bare commit would make the next rebuild
// a different extension with nothing saying so.
func TestDocumentDBRefIsAPinnedReleaseTag(t *testing.T) {
	ref := DocumentDBRef()
	if ref == "" {
		t.Fatal("documentDBRef must be pinned")
	}
	if !strings.HasPrefix(ref, "v") {
		t.Errorf("documentDBRef %q is not a release tag", ref)
	}
}

func TestParseCatalogRejectsAMissingDocumentDBRefWhenAMajorNeedsIt(t *testing.T) {
	_, err := parsePostgresCatalog([]byte(`
majors:
  - major: "17"
    baseImage: ghcr.io/x/y@sha256:aa
    documentdb: true
`))
	if err == nil {
		t.Fatal("claiming DocumentDB support with no pinned tag must be fatal")
	}
}

func TestPostgresCatalogEntriesIsACopy(t *testing.T) {
	entries := PostgresCatalogEntries()
	if len(entries) != len(PostgresMajors()) {
		t.Fatalf("entries: got %d, want %d", len(entries), len(PostgresMajors()))
	}
	entries[0].Major = "mutated"
	if PostgresCatalogEntries()[0].Major == "mutated" {
		t.Fatal("PostgresCatalogEntries handed out the backing array")
	}
}

func TestSupportedPostgresMajorsMessageListsEveryMajor(t *testing.T) {
	message := SupportedPostgresMajorsMessage()
	for _, major := range PostgresMajors() {
		if !strings.Contains(message, major) {
			t.Errorf("message %q omits major %s", message, major)
		}
	}
}

func TestDocumentDBMajorsMessageListsOnlyCapableMajors(t *testing.T) {
	majors := DocumentDBMajors()
	if len(majors) == 0 {
		t.Fatal("no major offers DocumentDB")
	}
	for _, major := range majors {
		if !DocumentDBSupported(major) {
			t.Errorf("major %s listed but not supported", major)
		}
	}
	message := DocumentDBMajorsMessage()
	for _, major := range majors {
		if !strings.Contains(message, major) {
			t.Errorf("message %q omits major %s", message, major)
		}
	}
	if strings.Contains(message, "14") {
		t.Errorf("message %q lists a major that cannot offer DocumentDB", message)
	}
}

func TestDockerPostgresImageUsesTheUpstreamImageForEveryMajor(t *testing.T) {
	for _, major := range PostgresMajors() {
		image, err := DockerPostgresImage(major)
		if err != nil {
			t.Errorf("major %s: %v", major, err)
			continue
		}
		if image != "postgres:"+major {
			t.Errorf("major %s: got %q", major, image)
		}
	}
}

func TestDockerPostgresImageRefusesAnUnsupportedMajor(t *testing.T) {
	for _, major := range []string{"", "13", "latest"} {
		if _, err := DockerPostgresImage(major); err == nil {
			t.Errorf("major %q: expected a refusal", major)
		}
	}
}

func TestPublishPostgresCatalogForTestPublishesAndRestores(t *testing.T) {
	before := PostgresCatalogEntries()

	restore := PublishPostgresCatalogForTest()
	for _, entry := range PostgresCatalogEntries() {
		image, err := PostgresImage(entry.Major)
		if err != nil {
			t.Errorf("major %s: %v", entry.Major, err)
			continue
		}
		if !digestRef.MatchString(image) {
			t.Errorf("major %s: published image %q is not digest-pinned", entry.Major, image)
		}
	}
	restore()

	after := PostgresCatalogEntries()
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("catalogue not restored at %d: %+v vs %+v", i, before[i], after[i])
		}
	}
}

func TestMustAtoiMajorRejectsANonNumericMajor(t *testing.T) {
	if got := mustAtoiMajor("17"); got != 17 {
		t.Errorf("got %d, want 17", got)
	}
	if got := mustAtoiMajor("seventeen"); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}
