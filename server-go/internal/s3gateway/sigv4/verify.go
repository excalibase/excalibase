package sigv4

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	algorithm      = "AWS4-HMAC-SHA256"
	amzDateLayout  = "20060102T150405Z"
	scopeDateSize  = len("20060102")
	scopeService   = "s3"
	scopeTerminal  = "aws4_request"
	signatureBytes = sha256.Size

	emptySHA256                     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload                 = "UNSIGNED-PAYLOAD"
	streamingSignedPayload          = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingSignedPayloadTrailer   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	streamingUnsignedPayloadTrailer = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"

	// DefaultMaxSkew is how far a request's date may be from the server's
	// clock, either way; S3's own window.
	DefaultMaxSkew = 15 * time.Minute
)

// PayloadMode is how the body is protected, as x-amz-content-sha256 declares.
type PayloadMode int

const (
	// PayloadSigned bodies carry their sha256 in the signed headers.
	PayloadSigned PayloadMode = iota
	// PayloadUnsigned bodies are protected by TLS only, plus any digest header.
	PayloadUnsigned
	// PayloadStreamingSigned bodies are aws-chunked with a signature per chunk.
	PayloadStreamingSigned
	// PayloadStreamingSignedTrailer adds a signed checksum trailer.
	PayloadStreamingSignedTrailer
	// PayloadStreamingUnsignedTrailer is aws-chunked with a checksum trailer
	// and no chunk signatures: what AWS SDKs send by default over HTTPS.
	PayloadStreamingUnsignedTrailer
)

// SecretLookup returns the secret for an access key id, ErrUnknownKey when
// there is none, or another error when it cannot tell.
type SecretLookup func(ctx context.Context, accessKeyID string) (string, error)

// Verifier checks header-signed SigV4 requests.
type Verifier struct {
	Lookup  SecretLookup
	Now     func() time.Time
	MaxSkew time.Duration
}

// Auth is a verified request: who signed it, and what the body must be read
// against.
type Auth struct {
	AccessKeyID string
	Payload     PayloadMode

	payloadHash string
	signingKey  []byte
	scope       string
	amzDate     string
	seed        string
}

type authorization struct {
	accessKeyID   string
	scopeDate     string
	region        string
	signedHeaders []string
	signature     string
}

// Verify checks the request's signature and date. It reads no body.
func (v *Verifier) Verify(r *http.Request) (*Auth, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		if r.URL.Query().Has("X-Amz-Signature") || r.URL.Query().Has("X-Amz-Algorithm") {
			return nil, s3Error(http.StatusForbidden, "AccessDenied", "presigned URLs are not supported")
		}
		return nil, s3Error(http.StatusForbidden, "AccessDenied", "anonymous requests are not allowed")
	}
	if !strings.HasPrefix(header, algorithm+" ") {
		return nil, s3Error(http.StatusForbidden, "AccessDenied", "only AWS Signature Version 4 (%s) is supported", algorithm)
	}
	parsed, err := parseAuthorization(strings.TrimPrefix(header, algorithm+" "))
	if err != nil {
		return nil, err
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		return nil, errInvalidRequest("x-amz-content-sha256 is required")
	}
	mode, err := payloadModeOf(payloadHash)
	if err != nil {
		return nil, err
	}
	amzDate, signedAt, err := requestDate(r, parsed)
	if err != nil {
		return nil, err
	}
	if err := v.checkSkew(signedAt); err != nil {
		return nil, err
	}
	return v.checkSignature(r, parsed, mode, payloadHash, amzDate)
}

func (v *Verifier) checkSkew(signedAt time.Time) error {
	maxSkew := v.MaxSkew
	if maxSkew <= 0 {
		maxSkew = DefaultMaxSkew
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	skew := now().Sub(signedAt)
	if skew > maxSkew || skew < -maxSkew {
		return s3Error(http.StatusForbidden, "RequestTimeTooSkewed",
			"the difference between the request time and the server's time is too large")
	}
	return nil
}

func (v *Verifier) checkSignature(r *http.Request, parsed authorization, mode PayloadMode, payloadHash, amzDate string) (*Auth, error) {
	secret, err := v.Lookup(r.Context(), parsed.accessKeyID)
	if errors.Is(err, ErrUnknownKey) {
		return nil, errDenied()
	}
	if err != nil {
		return nil, s3Error(http.StatusServiceUnavailable, "ServiceUnavailable", "please retry the request later")
	}
	query, ok := canonicalQuery(r.URL.RawQuery)
	if !ok {
		return nil, errInvalidRequest("the query string is not valid percent-encoding")
	}
	canonical := strings.Join([]string{
		r.Method,
		canonicalURI(r.URL),
		query,
		canonicalHeaders(r, parsed.signedHeaders),
		strings.Join(parsed.signedHeaders, ";"),
		payloadHash,
	}, "\n")
	scope := strings.Join([]string{parsed.scopeDate, parsed.region, scopeService, scopeTerminal}, "/")
	stringToSign := strings.Join([]string{algorithm, amzDate, scope, sha256Hex([]byte(canonical))}, "\n")
	key := signingKey(secret, parsed.scopeDate, parsed.region)
	if !signatureMatches(parsed.signature, hmacSHA256(key, []byte(stringToSign))) {
		return nil, errDenied()
	}
	return &Auth{
		AccessKeyID: parsed.accessKeyID,
		Payload:     mode,
		payloadHash: payloadHash,
		signingKey:  key,
		scope:       scope,
		amzDate:     amzDate,
		seed:        parsed.signature,
	}, nil
}

func parseAuthorization(value string) (authorization, error) {
	fields := map[string]string{}
	for _, part := range strings.Split(value, ",") {
		name, field, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return authorization{}, errMalformed("the authorization header is malformed")
		}
		fields[name] = field
	}
	credential := strings.Split(fields["Credential"], "/")
	if len(credential) != 5 || credential[0] == "" || len(credential[1]) != scopeDateSize || credential[2] == "" {
		return authorization{}, errMalformed("the credential must be <key>/<date>/<region>/s3/aws4_request")
	}
	if credential[3] != scopeService || credential[4] != scopeTerminal {
		return authorization{}, errMalformed("the credential scope must be for s3 and end in aws4_request")
	}
	signedHeaders := strings.Split(fields["SignedHeaders"], ";")
	if fields["SignedHeaders"] == "" {
		return authorization{}, errMalformed("SignedHeaders is required")
	}
	signature := fields["Signature"]
	if decoded, err := hex.DecodeString(signature); err != nil || len(decoded) != signatureBytes {
		return authorization{}, errMalformed("the signature must be %d hex bytes", signatureBytes)
	}
	return authorization{
		accessKeyID:   credential[0],
		scopeDate:     credential[1],
		region:        credential[2],
		signedHeaders: signedHeaders,
		signature:     signature,
	}, nil
}

// requestDate reads the signing date and insists that every header the
// signature must cover is in the signed list: the host, the payload hash (so
// the body's protection cannot be swapped) and the date itself.
func requestDate(r *http.Request, parsed authorization) (string, time.Time, error) {
	signed := map[string]bool{}
	for _, name := range parsed.signedHeaders {
		signed[name] = true
	}
	if !signed["host"] || !signed["x-amz-content-sha256"] {
		return "", time.Time{}, errMalformed("host and x-amz-content-sha256 must be signed")
	}
	amzDate := r.Header.Get("X-Amz-Date")
	dateHeader := "x-amz-date"
	if amzDate == "" {
		dateHeader = "date"
		if parsedDate, err := http.ParseTime(r.Header.Get("Date")); err == nil {
			amzDate = parsedDate.UTC().Format(amzDateLayout)
		}
	}
	if !signed[dateHeader] {
		return "", time.Time{}, errMalformed("the request date must be signed")
	}
	signedAt, err := time.Parse(amzDateLayout, amzDate)
	if err != nil {
		return "", time.Time{}, errMalformed("the request date is not valid")
	}
	if amzDate[:scopeDateSize] != parsed.scopeDate {
		return "", time.Time{}, errMalformed("the credential date must be the request's date")
	}
	return amzDate, signedAt, nil
}

func payloadModeOf(hash string) (PayloadMode, error) {
	switch hash {
	case unsignedPayload:
		return PayloadUnsigned, nil
	case streamingSignedPayload:
		return PayloadStreamingSigned, nil
	case streamingSignedPayloadTrailer:
		return PayloadStreamingSignedTrailer, nil
	case streamingUnsignedPayloadTrailer:
		return PayloadStreamingUnsignedTrailer, nil
	}
	if decoded, err := hex.DecodeString(hash); err == nil && len(decoded) == sha256.Size {
		return PayloadSigned, nil
	}
	return 0, s3Error(http.StatusBadRequest, "InvalidArgument", "x-amz-content-sha256 must be a sha256 or a known payload mode")
}

func signingKey(secret, date, region string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	key = hmacSHA256(key, []byte(region))
	key = hmacSHA256(key, []byte(scopeService))
	return hmacSHA256(key, []byte(scopeTerminal))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func signatureMatches(given string, computed []byte) bool {
	decoded, err := hex.DecodeString(given)
	return err == nil && hmac.Equal(decoded, computed)
}
