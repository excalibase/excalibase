package sigv4

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"net/http"
	"strings"
)

// crc64NVMETable is CRC-64/NVME (reflected polynomial 0x9a6c9329ac4bc9b5);
// Go's crc64 already starts from and finishes with all ones, as NVME does.
var crc64NVMETable = crc64.MakeTable(0x9a6c9329ac4bc9b5)

// newChecksum returns the hash for an x-amz-checksum-* header name, or nil
// for an algorithm this package does not know.
func newChecksum(header string) hash.Hash {
	switch strings.ToLower(header) {
	case "x-amz-checksum-crc32":
		return crc32.NewIEEE()
	case "x-amz-checksum-crc32c":
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case "x-amz-checksum-crc64nvme":
		return crc64.New(crc64NVMETable)
	case "x-amz-checksum-sha1":
		return sha1.New()
	case "x-amz-checksum-sha256":
		return sha256.New()
	}
	return nil
}

// digest is one value the body must hash to once it has all been read.
type digest struct {
	name     string
	hash     hash.Hash
	expected string // base64, except the payload sha256, which is hex
	encode   func([]byte) string
	failure  func() *Error
}

func (d *digest) matches() bool { return d.encode(d.hash.Sum(nil)) == d.expected }

func base64Digest(name string, h hash.Hash, expected string) *digest {
	return &digest{
		name: name, hash: h, expected: expected,
		encode:  base64.StdEncoding.EncodeToString,
		failure: func() *Error { return errBadDigest(name) },
	}
}

// headerDigests are the digests the client put in headers: Content-MD5 and
// at most one x-amz-checksum-*. An algorithm it names that is unknown here is
// refused rather than skipped.
func headerDigests(r *http.Request) ([]*digest, error) {
	var digests []*digest
	if value := r.Header.Get("Content-Md5"); value != "" {
		digests = append(digests, base64Digest("Content-MD5", md5.New(), value))
	}
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-checksum-") || lower == "x-amz-checksum-type" || lower == "x-amz-checksum-algorithm" {
			continue
		}
		h := newChecksum(lower)
		if h == nil {
			return nil, s3Error(http.StatusBadRequest, "InvalidRequest", "the checksum %s is not supported", lower)
		}
		digests = append(digests, base64Digest(lower, h, values[0]))
	}
	return digests, nil
}
