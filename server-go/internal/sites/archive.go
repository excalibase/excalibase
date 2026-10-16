package sites

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// Limits bound one deploy's build output. Files and file size are Cloudflare's
// own static-assets limits; the total keeps one upload off our disk's budget.
type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits is what every deploy is held to.
var DefaultLimits = Limits{
	MaxFiles:      20_000,
	MaxFileBytes:  25 << 20,
	MaxTotalBytes: 100 << 20,
}

const maxPathLength = 1024

// tarFramingBytes covers tar headers and padding on top of the files themselves.
const tarFramingBytes = 1024

// MaxArchiveBytes bounds the archive as read, compressed or not: the files plus framing for every entry.
func MaxArchiveBytes(limits Limits) int64 {
	return limits.MaxTotalBytes + int64(2*limits.MaxFiles+2)*tarFramingBytes
}

var errArchiveInflated = errors.New("archive larger than its limit once decompressed")

// cappedReader fails once more than limit bytes have been read through it.
type cappedReader struct {
	source io.Reader
	limit  int64
	read   int64
}

func (c *cappedReader) Read(buffer []byte) (int, error) {
	if c.read > c.limit {
		return 0, errArchiveInflated
	}
	if remaining := c.limit + 1 - c.read; int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	count, err := c.source.Read(buffer)
	c.read += int64(count)
	if c.read > c.limit {
		return count, errArchiveInflated
	}
	return count, err
}

// ArchiveError is a refusal of the uploaded archive, worded for the person who built it.
type ArchiveError struct {
	Reason   string
	TooLarge bool
	// cause is the read failure behind the refusal, such as the caller's body cap.
	cause error
}

func (e *ArchiveError) Error() string { return e.Reason }
func (e *ArchiveError) Unwrap() error { return e.cause }

func refuseRead(cause error, format string, args ...any) *ArchiveError {
	return &ArchiveError{Reason: fmt.Sprintf(format, args...), cause: cause}
}

func refuse(format string, args ...any) *ArchiveError {
	return &ArchiveError{Reason: fmt.Sprintf(format, args...)}
}

func refuseSize(format string, args ...any) *ArchiveError {
	return &ArchiveError{Reason: fmt.Sprintf(format, args...), TooLarge: true}
}

// BundleFile is one file of the build output, spooled to disk.
type BundleFile struct {
	Path        string
	Hash        string
	Size        int64
	ContentType string
	spool       string
}

// Bundle is a validated build output; Close removes its spool.
type Bundle struct {
	dir        string
	Files      []BundleFile
	TotalBytes int64
}

// Manifest is the bundle as Cloudflare's upload session reads it.
func (b *Bundle) Manifest() map[string]cfworkers.ManifestEntry {
	manifest := make(map[string]cfworkers.ManifestEntry, len(b.Files))
	for _, file := range b.Files {
		manifest[file.Path] = cfworkers.ManifestEntry{Hash: file.Hash, Size: file.Size}
	}
	return manifest
}

// Open reads one spooled file back.
func (b *Bundle) Open(file BundleFile) (io.ReadCloser, error) {
	return os.Open(filepath.Join(b.dir, file.spool))
}

func (b *Bundle) Close() error { return os.RemoveAll(b.dir) }

// ReadArchive streams a tar or tar.gz of build output into a spool under
// spoolDir, refusing anything that is not a plain tree of regular files.
func ReadArchive(body io.Reader, limits Limits, spoolDir string) (*Bundle, error) {
	dir, err := os.MkdirTemp(spoolDir, "site-deploy-")
	if err != nil {
		return nil, fmt.Errorf("create deploy spool: %w", err)
	}
	bundle := &Bundle{dir: dir}
	if err := bundle.fill(body, limits); err != nil {
		if cleanupErr := bundle.Close(); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		return nil, err
	}
	return bundle, nil
}

func (b *Bundle) fill(body io.Reader, limits Limits) error {
	stream, err := decompressed(body)
	if err != nil {
		return err
	}
	// Pax and GNU long-name headers never surface as entries, so the inflated stream itself is capped.
	reader := tar.NewReader(&cappedReader{source: stream, limit: MaxArchiveBytes(limits)})
	seen := map[string]bool{}
	for entries := 1; ; entries++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if errors.Is(err, errArchiveInflated) {
			return refuseSize("the archive is larger than %d bytes once decompressed", MaxArchiveBytes(limits))
		}
		if err != nil {
			return refuseRead(err, "the upload is not a readable tar or tar.gz archive: %v", err)
		}
		// Folders cost nothing to compress, so they are counted too.
		if entries > 2*limits.MaxFiles {
			return refuseSize("the archive holds more than %d entries", 2*limits.MaxFiles)
		}
		if err := b.addEntry(reader, header, limits, seen); err != nil {
			return err
		}
	}
	if !seen["/index.html"] {
		return refuse("the archive has no index.html at its root; build it with: tar -czf dist.tgz -C dist .")
	}
	return nil
}

// decompressed reads gzip when the body starts with its magic bytes, plain tar otherwise.
func decompressed(body io.Reader) (io.Reader, error) {
	buffered := bufio.NewReader(body)
	magic, err := buffered.Peek(2)
	if err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		unzipped, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, refuseRead(err, "the upload is not a readable gzip stream: %v", err)
		}
		return unzipped, nil
	}
	return buffered, nil
}

func (b *Bundle) addEntry(reader io.Reader, header *tar.Header, limits Limits, seen map[string]bool) error {
	if err := refuseEntryType(header); err != nil || header.Typeflag == tar.TypeXGlobalHeader {
		return err
	}
	sitePath, err := cleanPath(header.Name, header.Typeflag == tar.TypeDir)
	if err != nil || sitePath == "" {
		return err
	}
	if header.Typeflag == tar.TypeDir {
		return nil
	}
	if seen[sitePath] {
		return refuse("%s appears twice in the archive", sitePath)
	}
	seen[sitePath] = true
	if len(b.Files) >= limits.MaxFiles {
		return refuseSize("the archive holds more than %d files", limits.MaxFiles)
	}
	if header.Size > limits.MaxFileBytes {
		return refuseSize("%s is larger than %d bytes", sitePath, limits.MaxFileBytes)
	}
	if b.TotalBytes+header.Size > limits.MaxTotalBytes {
		return refuseSize("the files total more than %d bytes", limits.MaxTotalBytes)
	}
	return b.spoolFile(reader, sitePath, header.Size)
}

func refuseEntryType(header *tar.Header) error {
	switch header.Typeflag {
	case tar.TypeReg, tar.TypeDir, tar.TypeXGlobalHeader:
		return nil
	case tar.TypeSymlink:
		return refuse("%s is a symbolic link; the archive may hold only files and folders", header.Name)
	case tar.TypeLink:
		return refuse("%s is a hard link; the archive may hold only files and folders", header.Name)
	case tar.TypeChar, tar.TypeBlock:
		return refuse("%s is a device; the archive may hold only files and folders", header.Name)
	case tar.TypeFifo:
		return refuse("%s is a named pipe; the archive may hold only files and folders", header.Name)
	default:
		return refuse("%s has an entry type the archive may not hold", header.Name)
	}
}

// cleanPath turns a tar name into the site path ("/a/b.js"); "" is the archive root itself.
func cleanPath(name string, isDir bool) (string, error) {
	if len(name) > maxPathLength {
		return "", refuse("a path in the archive is longer than %d characters", maxPathLength)
	}
	if strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", refuse("%q contains a control character", name)
	}
	if strings.Contains(name, `\`) {
		return "", refuse("%s contains a backslash; use / between folders", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", refuse("%s is an absolute path; paths must be relative to the build folder", name)
	}
	trimmed := strings.TrimPrefix(name, "./")
	if isDir {
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", refuse("%s climbs out of the build folder with ..", name)
		}
		if segment == "" || segment == "." {
			return "", refuse("%s is not a plain relative path", name)
		}
	}
	return "/" + trimmed, nil
}

// spoolFile copies one file to disk while hashing it as Wrangler does:
// SHA-256 of base64(content) + extension, first 32 hex characters.
func (b *Bundle) spoolFile(reader io.Reader, sitePath string, declared int64) error {
	spool := strconv.Itoa(len(b.Files))
	out, err := os.OpenFile(filepath.Join(b.dir, spool), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("spool %s: %w", sitePath, err)
	}
	hasher := sha256.New()
	encoder := base64.NewEncoder(base64.StdEncoding, hasher)
	written, copyErr := io.Copy(io.MultiWriter(out, encoder), io.LimitReader(reader, declared))
	closeErr := out.Close()
	if errors.Is(copyErr, errArchiveInflated) {
		return refuseSize("the archive is larger than its limit once decompressed")
	}
	if copyErr != nil {
		return refuseRead(copyErr, "could not read %s from the archive: %v", sitePath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("spool %s: %w", sitePath, closeErr)
	}
	if written != declared {
		return refuse("%s is cut short in the archive", sitePath)
	}
	b.Files = append(b.Files, BundleFile{
		Path:        sitePath,
		Hash:        assetHash(encoder, hasher, sitePath),
		Size:        written,
		ContentType: contentTypeFor(sitePath),
		spool:       spool,
	})
	b.TotalBytes += written
	return nil
}

// assetHash finishes the base64 stream into the hash, then adds the extension.
func assetHash(encoder io.Closer, hasher hash.Hash, sitePath string) string {
	// Closing a base64 encoder over a hash only flushes padding, which cannot fail.
	_ = encoder.Close()
	hasher.Write([]byte(strings.TrimPrefix(path.Ext(sitePath), ".")))
	return hex.EncodeToString(hasher.Sum(nil))[:32]
}
