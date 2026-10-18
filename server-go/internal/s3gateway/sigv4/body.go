package sigv4

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Body is a request body read the way its signature says: exactly its
// declared length, decoded from aws-chunked when it is chunked, and checked
// against every digest the client declared. A body that breaks any of that
// fails its last Read with an *Error instead of io.EOF, so a caller that has
// streamed it somewhere must treat everything it wrote as unaccepted until
// Read has returned io.EOF.
type Body struct {
	reader io.Reader
	length int64
}

func (b *Body) Read(p []byte) (int, error) { return b.reader.Read(p) }

// Length is the decoded length of the payload.
func (b *Body) Length() int64 { return b.length }

func errMissingLength() *Error {
	return s3Error(http.StatusLengthRequired, "MissingContentLength", "the request must declare its length")
}

// Body returns the request's payload as its signature describes it.
func (a *Auth) Body(r *http.Request) (*Body, error) {
	digests, err := headerDigests(r)
	if err != nil {
		return nil, err
	}
	switch a.Payload {
	case PayloadSigned, PayloadUnsigned:
		return a.plainBody(r, digests)
	default:
		return a.chunkedBody(r, digests)
	}
}

func (a *Auth) plainBody(r *http.Request, digests []*digest) (*Body, error) {
	if r.ContentLength < 0 {
		return nil, errMissingLength()
	}
	if a.Payload == PayloadSigned {
		digests = append(digests, &digest{
			name: "x-amz-content-sha256", hash: sha256.New(), expected: a.payloadHash,
			encode: hex.EncodeToString,
			failure: func() *Error {
				return s3Error(http.StatusBadRequest, "XAmzContentSHA256Mismatch",
					"the provided x-amz-content-sha256 does not match what was computed")
			},
		})
	}
	return &Body{reader: newExactReader(bodyOf(r), r.ContentLength, digests), length: r.ContentLength}, nil
}

func (a *Auth) chunkedBody(r *http.Request, digests []*digest) (*Body, error) {
	declared := r.Header.Get("X-Amz-Decoded-Content-Length")
	if declared == "" {
		return nil, errMissingLength()
	}
	length, err := strconv.ParseInt(declared, 10, 64)
	if err != nil || length < 0 {
		return nil, errInvalidRequest("x-amz-decoded-content-length is not a length")
	}
	var signer *chunkSigner
	if a.Payload == PayloadStreamingSigned || a.Payload == PayloadStreamingSignedTrailer {
		signer = &chunkSigner{key: a.signingKey, scope: a.scope, amzDate: a.amzDate, previous: a.seed}
	}
	var trailer *trailerCheck
	if a.Payload == PayloadStreamingSignedTrailer || a.Payload == PayloadStreamingUnsignedTrailer {
		name := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Amz-Trailer")))
		if name != "" {
			h := newChecksum(name)
			if h == nil {
				return nil, errInvalidRequest("the trailer %s is not supported", name)
			}
			trailer = &trailerCheck{name: name, hash: h}
		}
	}
	return &Body{reader: newChunkedReader(bodyOf(r), length, signer, trailer, digests...), length: length}, nil
}

func bodyOf(r *http.Request) io.Reader {
	if r.Body == nil {
		return http.NoBody
	}
	return r.Body
}

// exactReader passes exactly length bytes, feeding every digest, and checks
// them once the last byte has gone.
type exactReader struct {
	src     io.Reader
	left    int64
	digests []*digest
	done    error
}

func newExactReader(src io.Reader, length int64, digests []*digest) *exactReader {
	return &exactReader{src: src, left: length, digests: digests}
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.done != nil {
		return 0, e.done
	}
	if e.left == 0 {
		e.done = e.finish()
		return 0, e.done
	}
	if int64(len(p)) > e.left {
		p = p[:e.left]
	}
	n, err := e.src.Read(p)
	e.left -= int64(n)
	for _, d := range e.digests {
		d.hash.Write(p[:n])
	}
	if errors.Is(err, io.EOF) && e.left > 0 {
		e.done = errIncomplete()
		return n, e.done
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	return n, nil
}

func (e *exactReader) finish() error {
	var extra [1]byte
	if n, _ := io.ReadFull(e.src, extra[:]); n > 0 {
		return errInvalidRequest("the request body is longer than its declared length")
	}
	return checkDigests(e.digests)
}

func checkDigests(digests []*digest) error {
	for _, d := range digests {
		if !d.matches() {
			return d.failure()
		}
	}
	return io.EOF
}
