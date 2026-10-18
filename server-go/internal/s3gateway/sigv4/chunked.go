package sigv4

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const (
	// maxChunkLine bounds a chunk header or trailer line; real ones are well
	// under 200 bytes.
	maxChunkLine = 4096
	// maxTrailerLines bounds the trailer section.
	maxTrailerLines      = 8
	chunkSignatureExt    = "chunk-signature="
	trailerSignatureName = "x-amz-trailer-signature"
)

// chunkSigner chains chunk signatures from the request's seed signature.
type chunkSigner struct {
	key      []byte
	scope    string
	amzDate  string
	previous string
}

func (s *chunkSigner) verify(kind, contentHash, given string) bool {
	stringToSign := strings.Join([]string{kind, s.amzDate, s.scope, s.previous, contentHash}, "\n")
	if !signatureMatches(given, hmacSHA256(s.key, []byte(stringToSign))) {
		return false
	}
	s.previous = given
	return true
}

func (s *chunkSigner) verifyChunk(given string, data hash.Hash) bool {
	return s.verify("AWS4-HMAC-SHA256-PAYLOAD", emptySHA256+"\n"+hex.EncodeToString(data.Sum(nil)), given)
}

// trailerCheck is the checksum the X-Amz-Trailer header promised.
type trailerCheck struct {
	name string
	hash hash.Hash
}

// chunkedReader decodes aws-chunked framing, streaming each chunk's data
// through as it arrives. A chunk's signature can only be checked once the
// chunk has been read, so a bad chunk surfaces as an error on a later Read;
// the Body contract (nothing is accepted before io.EOF) covers that.
type chunkedReader struct {
	src      *bufio.Reader
	declared int64
	decoded  int64
	signer   *chunkSigner
	trailer  *trailerCheck
	digests  []*digest

	chunkLeft int64
	chunkSig  string
	chunkHash hash.Hash
	done      error
}

func newChunkedReader(src io.Reader, declared int64, signer *chunkSigner, trailer *trailerCheck, digests ...*digest) *chunkedReader {
	return &chunkedReader{
		src: bufio.NewReaderSize(src, maxChunkLine), declared: declared,
		signer: signer, trailer: trailer, digests: digests, chunkLeft: -1,
	}
}

func errFraming(format string, args ...any) *Error {
	return errInvalidRequest("aws-chunked body: "+format, args...)
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.done != nil {
		return 0, c.done
	}
	n, err := c.read(p)
	if err != nil {
		c.done = err
	}
	return n, err
}

func (c *chunkedReader) read(p []byte) (int, error) {
	for c.chunkLeft <= 0 {
		if c.chunkLeft == 0 {
			if err := c.endChunk(); err != nil {
				return 0, err
			}
		}
		last, err := c.startChunk()
		if err != nil {
			return 0, err
		}
		if last {
			return 0, c.finish()
		}
	}
	if int64(len(p)) > c.chunkLeft {
		p = p[:c.chunkLeft]
	}
	n, err := c.src.Read(p)
	c.chunkLeft -= int64(n)
	c.decoded += int64(n)
	c.feed(p[:n])
	if errors.Is(err, io.EOF) {
		return n, errIncomplete()
	}
	return n, err
}

func (c *chunkedReader) feed(data []byte) {
	if c.chunkHash != nil {
		c.chunkHash.Write(data)
	}
	if c.trailer != nil {
		c.trailer.hash.Write(data)
	}
	for _, d := range c.digests {
		d.hash.Write(data)
	}
}

func (c *chunkedReader) readLine() (string, error) {
	line, err := c.src.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return "", errFraming("a line is longer than %d bytes", maxChunkLine)
	}
	if err != nil {
		return "", errIncomplete()
	}
	if !bytes.HasSuffix(line, []byte("\r\n")) {
		return "", errFraming("a line does not end in CRLF")
	}
	return string(line[:len(line)-2]), nil
}

// startChunk reads one chunk header; true means it was the final, empty chunk.
func (c *chunkedReader) startChunk() (bool, error) {
	line, err := c.readLine()
	if err != nil {
		return false, err
	}
	sizeField, extension, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(sizeField, 16, 64)
	if err != nil || size < 0 || len(sizeField) > 16 {
		return false, errFraming("a chunk size is not hex")
	}
	if size > c.declared-c.decoded {
		return false, errFraming("the chunks hold more than x-amz-decoded-content-length")
	}
	c.chunkSig = ""
	if c.signer != nil {
		if !strings.HasPrefix(extension, chunkSignatureExt) {
			return false, errFraming("a chunk has no signature")
		}
		c.chunkSig = strings.TrimPrefix(extension, chunkSignatureExt)
		c.chunkHash = sha256.New()
	}
	c.chunkLeft = size
	if size == 0 {
		return true, c.checkChunkSignature()
	}
	return false, nil
}

func (c *chunkedReader) endChunk() error {
	if err := c.checkChunkSignature(); err != nil {
		return err
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	if line != "" {
		return errFraming("chunk data is longer than its size")
	}
	c.chunkLeft = -1
	return nil
}

func (c *chunkedReader) checkChunkSignature() error {
	if c.signer == nil {
		return nil
	}
	if !c.signer.verifyChunk(c.chunkSig, c.chunkHash) {
		return s3Error(http.StatusForbidden, "SignatureDoesNotMatch", "a chunk signature does not match")
	}
	return nil
}

// finish reads the trailer section after the final chunk and checks the
// length, the trailer checksum and every header digest.
func (c *chunkedReader) finish() error {
	if c.decoded != c.declared {
		return errIncomplete()
	}
	trailers, raw, err := c.readTrailers()
	if err != nil {
		return err
	}
	if err := c.checkTrailer(trailers, raw); err != nil {
		return err
	}
	if n, _ := io.ReadFull(c.src, make([]byte, 1)); n > 0 {
		return errFraming("bytes follow the final chunk")
	}
	return checkDigests(c.digests)
}

func (c *chunkedReader) readTrailers() (map[string]string, string, error) {
	trailers := map[string]string{}
	var raw strings.Builder
	for i := 0; ; i++ {
		line, err := c.readLine()
		if err != nil {
			return nil, "", err
		}
		if line == "" {
			return trailers, raw.String(), nil
		}
		if i >= maxTrailerLines {
			return nil, "", errFraming("too many trailer lines")
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", s3Error(http.StatusBadRequest, "MalformedTrailerError", "a trailer line is malformed")
		}
		name = strings.ToLower(strings.TrimSpace(name))
		trailers[name] = strings.TrimSpace(value)
		if name != trailerSignatureName {
			raw.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
		}
	}
}

func (c *chunkedReader) checkTrailer(trailers map[string]string, raw string) error {
	if c.trailer == nil {
		if len(trailers) > 0 {
			return s3Error(http.StatusBadRequest, "MalformedTrailerError", "the request declared no trailer")
		}
		return nil
	}
	value, ok := trailers[c.trailer.name]
	if !ok || len(trailers) > 2 {
		return s3Error(http.StatusBadRequest, "MalformedTrailerError", "the trailer %s is missing", c.trailer.name)
	}
	if c.signer != nil {
		given := trailers[trailerSignatureName]
		if !c.signer.verify("AWS4-HMAC-SHA256-TRAILER", sha256Hex([]byte(raw)), given) {
			return s3Error(http.StatusForbidden, "SignatureDoesNotMatch", "the trailer signature does not match")
		}
	}
	if base64.StdEncoding.EncodeToString(c.trailer.hash.Sum(nil)) != value {
		return errBadDigest(c.trailer.name)
	}
	return nil
}
