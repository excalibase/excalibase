package service

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-531: a DocumentDB project's documents live in the postgres database,
// which a pg_dump of the project database leaves out. Its export is one tar:
// the project database's dump plus every collection in mongodump's layout.

type fakeDumper struct {
	files map[string]string
	err   error
	calls int
}

func (f *fakeDumper) DumpDocuments(_ context.Context, _ string, sink docbrowser.DumpSink) error {
	f.calls++
	for name, content := range f.files {
		if err := sink.WriteFile(name, func(w io.Writer) error {
			_, err := io.WriteString(w, content)
			return err
		}); err != nil {
			return err
		}
	}
	return f.err
}

const snapshotDump = "PGDMP-binary-dump"

func documentSnapshotService(t *testing.T, documentDB bool, dumper DocumentDumper) (*SnapshotService, *k8s.MockClient) {
	t.Helper()
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	mock.ExecOutput["org-doc/doc-db-postgres-1"] = snapshotDump
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: "doc-db", Namespace: "org-doc", Status: "ACTIVE",
		DatabaseName: "app", DocumentDB: documentDB,
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewSnapshotService(store, mock, dir)
	if dumper != nil {
		svc.SetDocumentDumper(dumper)
	}
	return svc, mock
}

func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	files := map[string]string{}
	reader := tar.NewReader(bytes.NewReader(data))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name] = string(content)
	}
}

func TestADocumentDBExportBundlesTheDatabaseDumpAndEveryCollection(t *testing.T) {
	dumper := &fakeDumper{files: map[string]string{
		"shop/orders.bson": "BSON", "shop/orders.metadata.json": `{"collectionName":"orders"}`,
	}}
	svc, _ := documentSnapshotService(t, true, dumper)

	info, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !info.Documents {
		t.Error("the snapshot must say it carries documents")
	}
	data, filename, err := svc.DownloadSnapshot("doc-db", info.ID)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if !strings.HasSuffix(filename, ".tar") || info.Size != int64(len(data)) {
		t.Fatalf("filename %q size %d for %d bytes", filename, info.Size, len(data))
	}
	files := untar(t, data)
	want := map[string]string{
		"app.dump": snapshotDump, "mongo/shop/orders.bson": "BSON",
		"mongo/shop/orders.metadata.json": `{"collectionName":"orders"}`,
	}
	for name, content := range want {
		if files[name] != content {
			t.Errorf("%s = %q, want %q (files: %v)", name, files[name], content, files)
		}
	}
}

func TestADocumentDBExportIsRefusedWithoutADocumentDumper(t *testing.T) {
	svc, _ := documentSnapshotService(t, true, nil)
	if _, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{}); err == nil {
		t.Fatal("an export that would leave the documents out must fail")
	}
	assertNoSnapshots(t, svc)
}

func TestADocumentDBExportFailsWhenTheDocumentsCannotBeRead(t *testing.T) {
	svc, _ := documentSnapshotService(t, true, &fakeDumper{err: errors.New("gateway down")})
	if _, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{}); err == nil {
		t.Fatal("a failed document dump must fail the export")
	}
	assertNoSnapshots(t, svc)
}

func TestAPostgresExportNeverAsksForDocuments(t *testing.T) {
	dumper := &fakeDumper{}
	svc, _ := documentSnapshotService(t, false, dumper)
	info, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if dumper.calls != 0 || info.Documents {
		t.Fatalf("a Postgres project has no documents to dump (calls=%d)", dumper.calls)
	}
}

func TestADataOnlyExportDumpsDataAndBothOnlyChoicesAreRefused(t *testing.T) {
	svc, mock := documentSnapshotService(t, false, nil)
	info, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{DataOnly: true})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !info.DataOnly || info.SchemaOnly {
		t.Fatalf("the snapshot must record what it holds: %+v", info)
	}
	if last := mock.ExecCommands[len(mock.ExecCommands)-1]; !strings.Contains(last, "--data-only") || !strings.Contains(last, "-d app") {
		t.Fatalf("pg_dump = %q", last)
	}
	_, err = svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{DataOnly: true, SchemaOnly: true})
	if !errors.Is(err, ErrInvalidSnapshotRequest) {
		t.Fatalf("error = %v, want ErrInvalidSnapshotRequest", err)
	}
	if _, err := svc.ExportSnapshot(context.Background(), "doc-db", domain.SnapshotExportRequest{Format: "directory"}); !errors.Is(err, ErrInvalidSnapshotRequest) {
		t.Fatalf("error = %v, want ErrInvalidSnapshotRequest", err)
	}
}

func assertNoSnapshots(t *testing.T, svc *SnapshotService) {
	t.Helper()
	list, err := svc.ListSnapshots("doc-db")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("a failed export left %d snapshots listed", len(list))
	}
}
