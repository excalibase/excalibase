package sigv4

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func readAll(t *testing.T, body io.Reader) ([]byte, error) {
	t.Helper()
	return io.ReadAll(body)
}

func TestBody_SignedPayloadIsCheckedAgainstItsHash(t *testing.T) {
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"), nil, signedAt, "")
	auth, err := verifierAt(signedAt).Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Length() != 5 {
		t.Fatalf("length = %d", body.Length())
	}
	got, err := readAll(t, body)
	if err != nil || string(got) != "hello" {
		t.Fatalf("read %q, %v", got, err)
	}
}

func TestBody_SignedPayloadMismatchIsRefusedAtTheEnd(t *testing.T) {
	sum := sha256.Sum256([]byte("hello"))
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"), nil, signedAt, hex.EncodeToString(sum[:]))
	req.Body = io.NopCloser(strings.NewReader("HELLO"))
	auth, err := verifierAt(signedAt).Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readAll(t, body)
	wantCode(t, err, "XAmzContentSHA256Mismatch")
}

func TestBody_UnsignedPayloadNeedsALength(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "http://s3.example.test/photos/a.txt", strings.NewReader("hello"))
	req.ContentLength = -1
	_, err := (&Auth{Payload: PayloadUnsigned}).Body(req)
	wantCode(t, err, "MissingContentLength")
}

func TestBody_ShortBodyIsIncomplete(t *testing.T) {
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"), nil, signedAt, unsignedPayload)
	req.Body = io.NopCloser(strings.NewReader("hel"))
	auth, err := verifierAt(signedAt).Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readAll(t, body)
	wantCode(t, err, "IncompleteBody")
}

func TestBody_ContentMD5IsChecked(t *testing.T) {
	good := md5.Sum([]byte("hello"))
	bad := md5.Sum([]byte("HELLO"))
	for name, digest := range map[string][16]byte{"matching": good, "mismatching": bad} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"Content-Md5": base64.StdEncoding.EncodeToString(digest[:])}
			req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"), headers, signedAt, unsignedPayload)
			auth, err := verifierAt(signedAt).Verify(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := auth.Body(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = readAll(t, body)
			if name == "matching" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantCode(t, err, "BadDigest")
		})
	}
}

func TestBody_ChecksumHeaderIsChecked(t *testing.T) {
	crc := crc32.ChecksumIEEE([]byte("hello"))
	good := base64.StdEncoding.EncodeToString([]byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)})
	for name, value := range map[string]string{"matching": good, "mismatching": "AAAAAA=="} {
		t.Run(name, func(t *testing.T) {
			headers := map[string]string{"X-Amz-Checksum-Crc32": value}
			req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"), headers, signedAt, unsignedPayload)
			auth, err := verifierAt(signedAt).Verify(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := auth.Body(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = readAll(t, body)
			if name == "matching" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantCode(t, err, "BadDigest")
		})
	}
}

// awsChunkedExample is the streaming PUT from the AWS documentation
// ("Signature Calculations for the Authorization Header: Transferring Payload
// in Multiple Chunks"): 65536 + 1024 bytes of 'a', with the published
// signatures for every chunk.
func awsChunkedExample(t *testing.T, tamper func(chunks [][]byte)) *http.Request {
	t.Helper()
	chunks := [][]byte{bytes.Repeat([]byte("a"), 65536), bytes.Repeat([]byte("a"), 1024), {}}
	signatures := []string{
		"ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648",
		"0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497",
		"b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9",
	}
	if tamper != nil {
		tamper(chunks)
	}
	var body bytes.Buffer
	for i, chunk := range chunks {
		fmt.Fprintf(&body, "%x;chunk-signature=%s\r\n", len(chunk), signatures[i])
		body.Write(chunk)
		body.WriteString("\r\n")
	}
	req := httptest.NewRequest(http.MethodPut, "http://s3.amazonaws.com/examplebucket/chunkObject.txt", bytes.NewReader(body.Bytes()))
	req.Host = "s3.amazonaws.com"
	req.Header.Set("X-Amz-Date", "20130524T000000Z")
	req.Header.Set("X-Amz-Storage-Class", "REDUCED_REDUNDANCY")
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("X-Amz-Decoded-Content-Length", "66560")
	req.Header.Set("Content-Length", "66824")
	req.Header.Set("X-Amz-Content-Sha256", streamingSignedPayload)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+exampleKeyID+"/20130524/us-east-1/s3/aws4_request,"+
		"SignedHeaders=content-encoding;content-length;host;x-amz-content-sha256;x-amz-date;x-amz-decoded-content-length;x-amz-storage-class,"+
		"Signature=4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9")
	return req
}

var exampleTime = time.Date(2013, 5, 24, 0, 1, 0, 0, time.UTC)

func TestBody_AWSDocumentedChunkedExample(t *testing.T) {
	req := awsChunkedExample(t, nil)
	if req.ContentLength != 66824 {
		t.Fatalf("the example body is %d bytes, the documentation says 66824", req.ContentLength)
	}
	auth, err := verifierAt(exampleTime).Verify(req)
	if err != nil {
		t.Fatalf("seed signature refused: %v", err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	if body.Length() != 66560 {
		t.Fatalf("decoded length = %d", body.Length())
	}
	got, err := readAll(t, body)
	if err != nil {
		t.Fatalf("chunks refused: %v", err)
	}
	if !bytes.Equal(got, bytes.Repeat([]byte("a"), 66560)) {
		t.Fatalf("decoded %d bytes that are not the payload", len(got))
	}
}

func TestBody_TamperedChunkIsRefused(t *testing.T) {
	req := awsChunkedExample(t, func(chunks [][]byte) { chunks[1][0] = 'b' })
	auth, err := verifierAt(exampleTime).Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readAll(t, body)
	wantCode(t, err, "SignatureDoesNotMatch")
}

func TestBody_ChunkedDecodedLengthMustMatch(t *testing.T) {
	for name, declared := range map[string]string{"fewer bytes than declared": "6", "more bytes than declared": "4"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "http://s3.example.test/photos/a.txt",
				bytes.NewReader(unsignedTrailerBody([]byte("hello"), "")))
			req.Header.Set("Content-Encoding", "aws-chunked")
			req.Header.Set("X-Amz-Decoded-Content-Length", declared)
			body, err := (&Auth{Payload: PayloadStreamingUnsignedTrailer}).Body(req)
			if err != nil {
				t.Fatal(err)
			}
			_, err = readAll(t, body)
			var s3err *Error
			if !errors.As(err, &s3err) {
				t.Fatalf("err = %v, want an S3 error", err)
			}
		})
	}
}

func TestBody_ChunkedNeedsADecodedLength(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "http://s3.example.test/photos/a.txt",
		bytes.NewReader(unsignedTrailerBody([]byte("hello"), "")))
	req.Header.Set("Content-Encoding", "aws-chunked")
	_, err := (&Auth{Payload: PayloadStreamingUnsignedTrailer}).Body(req)
	wantCode(t, err, "MissingContentLength")
}

// unsignedTrailerBody builds what the AWS SDKs send by default since 2025:
// aws-chunked frames without signatures and a checksum trailer.
func unsignedTrailerBody(payload []byte, trailer string) []byte {
	var body bytes.Buffer
	if len(payload) > 0 {
		fmt.Fprintf(&body, "%x\r\n", len(payload))
		body.Write(payload)
		body.WriteString("\r\n")
	}
	body.WriteString("0\r\n")
	body.WriteString(trailer)
	body.WriteString("\r\n")
	return body.Bytes()
}

func unsignedTrailerRequest(t *testing.T, payload []byte, trailerName, trailer string) *http.Request {
	t.Helper()
	encoded := unsignedTrailerBody(payload, trailer)
	headers := map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Decoded-Content-Length": strconv.Itoa(len(payload)),
		"X-Amz-Trailer":                trailerName,
	}
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", encoded, headers, signedAt, streamingUnsignedPayloadTrailer)
	return req
}

func crc32Base64(data []byte) string {
	crc := crc32.ChecksumIEEE(data)
	return base64.StdEncoding.EncodeToString([]byte{byte(crc >> 24), byte(crc >> 16), byte(crc >> 8), byte(crc)})
}

func TestBody_UnsignedTrailerChecksum(t *testing.T) {
	payload := []byte("hello trailer")
	cases := map[string]struct {
		trailer string
		code    string
	}{
		"matching":     {"x-amz-checksum-crc32:" + crc32Base64(payload) + "\r\n", ""},
		"mismatching":  {"x-amz-checksum-crc32:AAAAAA==\r\n", "BadDigest"},
		"missing":      {"", "MalformedTrailerError"},
		"another name": {"x-amz-checksum-sha1:" + crc32Base64(payload) + "\r\n", "MalformedTrailerError"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			req := unsignedTrailerRequest(t, payload, "x-amz-checksum-crc32", tc.trailer)
			auth, err := verifierAt(signedAt).Verify(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := auth.Body(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readAll(t, body)
			if tc.code == "" {
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("read %q, %v", got, err)
				}
				return
			}
			wantCode(t, err, tc.code)
		})
	}
}

func TestBody_UnsignedTrailerEmptyObject(t *testing.T) {
	req := unsignedTrailerRequest(t, nil, "x-amz-checksum-crc32", "x-amz-checksum-crc32:"+crc32Base64(nil)+"\r\n")
	auth, err := verifierAt(signedAt).Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := auth.Body(req)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := readAll(t, body); err != nil || len(got) != 0 {
		t.Fatalf("read %q, %v", got, err)
	}
}

func TestBody_ChunkedFramingErrors(t *testing.T) {
	cases := map[string][]byte{
		"size is not hex":        []byte("zz\r\nhello\r\n0\r\n\r\n"),
		"size larger than limit": []byte("ffffffffffff\r\nhello\r\n0\r\n\r\n"),
		"no crlf after data":     []byte("5\r\nhelloX0\r\n\r\n"),
		"truncated":              []byte("5\r\nhel"),
		"more than declared":     []byte("6\r\nhello!\r\n0\r\n\r\n"),
		"overlong size line":     append(bytes.Repeat([]byte("0"), 5000), []byte("5\r\nhello\r\n0\r\n\r\n")...),
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "http://s3.example.test/photos/a.txt", bytes.NewReader(encoded))
			req.Header.Set("Content-Encoding", "aws-chunked")
			req.Header.Set("X-Amz-Decoded-Content-Length", "5")
			auth := &Auth{Payload: PayloadStreamingUnsignedTrailer}
			body, err := auth.Body(req)
			if err != nil {
				wantCode(t, err, "InvalidRequest")
				return
			}
			_, err = readAll(t, body)
			var s3err *Error
			if !errors.As(err, &s3err) {
				t.Fatalf("err = %v, want an S3 error", err)
			}
		})
	}
}

func TestChecksum_CRC64NVMEKnownValue(t *testing.T) {
	sum := newChecksum("x-amz-checksum-crc64nvme")
	if sum == nil {
		t.Fatal("crc64nvme is supported")
	}
	sum.Write([]byte("123456789"))
	got := hex.EncodeToString(sum.Sum(nil))
	if got != "ae8b14860a799888" {
		t.Fatalf("crc64nvme(123456789) = %s, want ae8b14860a799888", got)
	}
}

func TestChecksum_CRC32CKnownValue(t *testing.T) {
	sum := newChecksum("x-amz-checksum-crc32c")
	sum.Write([]byte("123456789"))
	if got := hex.EncodeToString(sum.Sum(nil)); got != "e3069283" {
		t.Fatalf("crc32c(123456789) = %s", got)
	}
}

func FuzzChunkedDecoder(f *testing.F) {
	f.Add([]byte("5\r\nhello\r\n0\r\n\r\n"), int64(5))
	f.Add([]byte("5;chunk-signature=00\r\nhello\r\n0;chunk-signature=00\r\n\r\n"), int64(5))
	f.Add([]byte("0\r\nx-amz-checksum-crc32:AAAAAA==\r\n\r\n"), int64(0))
	f.Fuzz(func(t *testing.T, encoded []byte, length int64) {
		if length < 0 || length > 1<<20 {
			return
		}
		decoder := newChunkedReader(bytes.NewReader(encoded), length, nil, nil)
		got, err := io.ReadAll(io.LimitReader(decoder, 2<<20))
		if err == nil && int64(len(got)) != length {
			t.Fatalf("accepted %d bytes for a declared %d", len(got), length)
		}
	})
}

// signedTrailerRequest signs chunks and a trailer the way the specification
// describes, chained from the SDK's seed signature. The chunk algorithm itself
// is pinned by the documented example above; this covers the trailer step.
func signedTrailerRequest(t *testing.T, payload []byte, tamperTrailer bool) *http.Request {
	t.Helper()
	crc := crc32Base64(payload)
	headers := map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Decoded-Content-Length": strconv.Itoa(len(payload)),
		"X-Amz-Trailer":                "x-amz-checksum-crc32",
	}
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", nil, headers, signedAt, streamingSignedPayloadTrailer)
	seed := req.Header.Get("Authorization")[len(req.Header.Get("Authorization"))-64:]
	amzDate := req.Header.Get("X-Amz-Date")
	scope := amzDate[:8] + "/us-east-1/s3/aws4_request"
	key := signingKey(exampleSecret, amzDate[:8], "us-east-1")
	sign := func(kind, previous, contentHash string) string {
		return hex.EncodeToString(hmacSHA256(key, []byte(kind+"\n"+amzDate+"\n"+scope+"\n"+previous+"\n"+contentHash)))
	}
	first := sign("AWS4-HMAC-SHA256-PAYLOAD", seed, emptySHA256+"\n"+sha256Hex(payload))
	final := sign("AWS4-HMAC-SHA256-PAYLOAD", first, emptySHA256+"\n"+emptySHA256)
	trailerLine := "x-amz-checksum-crc32:" + crc
	trailerSig := sign("AWS4-HMAC-SHA256-TRAILER", final, sha256Hex([]byte(trailerLine+"\n")))
	if tamperTrailer {
		trailerLine = "x-amz-checksum-crc32:AAAAAA=="
	}
	var body bytes.Buffer
	fmt.Fprintf(&body, "%x;chunk-signature=%s\r\n", len(payload), first)
	body.Write(payload)
	fmt.Fprintf(&body, "\r\n0;chunk-signature=%s\r\n%s\r\nx-amz-trailer-signature:%s\r\n\r\n", final, trailerLine, trailerSig)
	req.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	return req
}

func TestBody_SignedTrailer(t *testing.T) {
	payload := []byte("signed trailer payload")
	for name, tamper := range map[string]bool{"intact": false, "tampered trailer": true} {
		t.Run(name, func(t *testing.T) {
			req := signedTrailerRequest(t, payload, tamper)
			auth, err := verifierAt(signedAt).Verify(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := auth.Body(req)
			if err != nil {
				t.Fatal(err)
			}
			got, err := readAll(t, body)
			if !tamper {
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("read %q, %v", got, err)
				}
				return
			}
			wantCode(t, err, "SignatureDoesNotMatch")
		})
	}
}
