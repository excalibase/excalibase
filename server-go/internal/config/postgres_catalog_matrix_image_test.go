package config

import (
	"encoding/json"
	"testing"
)

// EXC-433: the workflow decides whether to build from what the catalogue
// pins, so the matrix carries it. Two runs of one commit gave two digests
// for major 14.
func TestBuildMatrixCarriesThePinnedImage(t *testing.T) {
	raw, err := RenderBuildMatrix()
	if err != nil {
		t.Fatalf("RenderBuildMatrix: %v", err)
	}
	var rows []BuildMatrixEntry
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byMajor := map[string]BuildMatrixEntry{}
	for _, row := range rows {
		byMajor[row.Major] = row
	}
	for _, entry := range postgresCatalog.Majors {
		row, present := byMajor[entry.Major]
		if !present {
			t.Errorf("major %s is missing from the build matrix", entry.Major)
			continue
		}
		if row.Image != entry.Image {
			t.Errorf("major %s: matrix image %q, catalogue pins %q",
				entry.Major, row.Image, entry.Image)
		}
	}
}

// An unpublished major carries no image, which is what tells the workflow to
// build and push it.
func TestBuildMatrixLeavesAnUnpublishedMajorWithoutAnImage(t *testing.T) {
	rendered, err := renderBuildMatrix(PostgresCatalog{
		Majors: []PostgresMajorEntry{{Major: "17", BaseImage: "ghcr.io/x/y@sha256:aa"}},
	})
	if err != nil {
		t.Fatalf("renderBuildMatrix: %v", err)
	}
	var rows []BuildMatrixEntry
	if err := json.Unmarshal(rendered, &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows: got %d, want 1", len(rows))
	}
	if rows[0].Image != "" {
		t.Errorf("image: got %q, want empty for an unpublished major", rows[0].Image)
	}
}
