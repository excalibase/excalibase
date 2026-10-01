package service

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"
)

// A DocumentDB project's export is one tar (EXC-531): the project database's
// dump at the top, and the documents under mongo/ in mongodump's directory
// layout, so `tar x` then `mongorestore --dir mongo` imports them back.

const (
	bundleExtension = ".tar"
	bundleMongoDir  = "mongo"
)

// writeDocumentBundle writes the tar to filePath and returns its size.
func (s *SnapshotService) writeDocumentBundle(ctx context.Context, projectID, filePath, dumpName string, dump []byte) (int64, error) {
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return 0, err
	}
	archive := tar.NewWriter(file)
	err = writeTarEntry(archive, dumpName, int64(len(dump)), func(w io.Writer) error {
		_, err := w.Write(dump)
		return err
	})
	if err == nil {
		err = s.documents.DumpDocuments(ctx, projectID, &tarSink{archive: archive, scratch: filepath.Dir(filePath)})
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		return 0, err
	}
	return stat.Size(), nil
}

// tarSink adds each dumped file under mongo/. A tar header needs the size
// first, so a file is written to a scratch file and then copied in.
type tarSink struct {
	archive *tar.Writer
	scratch string
}

func (t *tarSink) WriteFile(name string, write func(io.Writer) error) error {
	scratch, err := os.CreateTemp(t.scratch, ".dump-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(scratch.Name()) }()
	defer func() { _ = scratch.Close() }()
	if err := write(scratch); err != nil {
		return fmt.Errorf("dump %s: %w", name, err)
	}
	size, err := scratch.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := scratch.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return writeTarEntry(t.archive, path.Join(bundleMongoDir, name), size, func(w io.Writer) error {
		_, err := io.Copy(w, scratch)
		return err
	})
}

func writeTarEntry(archive *tar.Writer, name string, size int64, write func(io.Writer) error) error {
	header := &tar.Header{Name: name, Mode: 0644, Size: size, ModTime: time.Now(), Typeflag: tar.TypeReg}
	if err := archive.WriteHeader(header); err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}
	return write(archive)
}
