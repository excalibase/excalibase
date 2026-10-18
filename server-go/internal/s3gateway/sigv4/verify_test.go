package sigv4

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	// The access key id is not part of what SigV4 signs, so the documented
	// examples verify under any id; ours keeps secret scanners quiet.
	exampleKeyID  = "EXS3EXAMPLEKEYID0000"
	exampleSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

var errVaultSealed = errors.New("vault is sealed")

func lookupOf(keys map[string]string) SecretLookup {
	return func(_ context.Context, accessKeyID string) (string, error) {
		secret, ok := keys[accessKeyID]
		if !ok {
			return "", ErrUnknownKey
		}
		return secret, nil
	}
}

func verifierAt(now time.Time) *Verifier {
	return &Verifier{
		Authenticate: SecretAuthenticator(lookupOf(map[string]string{exampleKeyID: exampleSecret})),
		Now:    func() time.Time { return now },
	}
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var s3err *Error
	if !errors.As(err, &s3err) {
		t.Fatalf("err = %v, want an *Error with code %s", err, code)
	}
	if s3err.Code != code {
		t.Fatalf("code = %s (%s), want %s", s3err.Code, s3err.Message, code)
	}
}

// The GET Object example from the AWS SigV4 documentation: an independent
// account of the algorithm, so the verifier is not only checked against itself.
func TestVerify_AWSDocumentedGetObjectExample(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Host = "examplebucket.s3.amazonaws.com"
	req.Header.Set("Range", "bytes=0-9")
	req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
	req.Header.Set("X-Amz-Date", "20130524T000000Z")
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+exampleKeyID+"/20130524/us-east-1/s3/aws4_request,"+
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,"+
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41")

	auth, err := verifierAt(time.Date(2013, 5, 24, 0, 5, 0, 0, time.UTC)).Verify(req)
	if err != nil {
		t.Fatalf("documented example refused: %v", err)
	}
	if auth.AccessKeyID != exampleKeyID || auth.Payload != PayloadSigned {
		t.Fatalf("auth = %+v", auth)
	}
}

// sdkSigned signs a request exactly as the AWS SDK for Go v2 S3 client does
// and hands it to the verifier as the server would receive it.
func sdkSigned(t *testing.T, method, rawURL string, body []byte, headers map[string]string, at time.Time, payloadHash string) *http.Request {
	t.Helper()
	client, err := http.NewRequest(method, rawURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range headers {
		client.Header.Set(name, value)
	}
	if payloadHash == "" {
		sum := sha256.Sum256(body)
		payloadHash = hex.EncodeToString(sum[:])
	}
	client.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signer := v4.NewSigner(func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	creds := aws.Credentials{AccessKeyID: exampleKeyID, SecretAccessKey: exampleSecret}
	if err := signer.SignHTTP(context.Background(), creds, client, payloadHash, "s3", "us-east-1", at); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewRequest(method, rawURL, bytes.NewReader(body))
	server.Header = client.Header.Clone()
	return server
}

var signedAt = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func TestVerify_AcceptsWhatTheSDKSigns(t *testing.T) {
	cases := []struct {
		name, method, url string
		body              []byte
		headers           map[string]string
	}{
		{"list with a query", http.MethodGet, "http://s3.example.test/photos?delimiter=%2F&list-type=2&prefix=a%20b%2F", nil, nil},
		{"key with spaces and unicode", http.MethodGet, "http://s3.example.test/photos/dir/a%20b%E2%9C%93.png", nil, nil},
		{"key with reserved characters", http.MethodHead, "http://s3.example.test/photos/a%2Bb%3Dc%21%27%28%29.txt", nil, nil},
		{"multipart create", http.MethodPost, "http://s3.example.test/photos/big.bin?uploads", nil, nil},
		{"put with a body and metadata", http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"),
			map[string]string{"Content-Type": "text/plain", "X-Amz-Meta-Mtime": "1700000000.5"}},
		{"bucket listing", http.MethodGet, "http://s3.example.test/", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := sdkSigned(t, tc.method, tc.url, tc.body, tc.headers, signedAt, "")
			if _, err := verifierAt(signedAt).Verify(req); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

func TestVerify_RefusesATamperedRequest(t *testing.T) {
	tamper := map[string]func(r *http.Request){
		"method":        func(r *http.Request) { r.Method = http.MethodDelete },
		"path":          func(r *http.Request) { r.URL.Path = "/other/a.txt"; r.URL.RawPath = "" },
		"query":         func(r *http.Request) { r.URL.RawQuery = "prefix=secret" },
		"signed header": func(r *http.Request) { r.Header.Set("Content-Type", "text/html") },
		"host":          func(r *http.Request) { r.Host = "evil.example.test" },
		"payload hash":  func(r *http.Request) { r.Header.Set("X-Amz-Content-Sha256", unsignedPayload) },
		"signature":     func(r *http.Request) { r.Header.Set("Authorization", flipLastHex(r.Header.Get("Authorization"))) },
		"credential key": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), exampleKeyID, "EXS3OTHERKEY00000000", 1))
		},
	}
	for name, change := range tamper {
		t.Run(name, func(t *testing.T) {
			req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", []byte("hello"),
				map[string]string{"Content-Type": "text/plain"}, signedAt, "")
			change(req)
			_, err := verifierAt(signedAt).Verify(req)
			wantCode(t, err, "AccessDenied")
		})
	}
}

func TestVerify_ClockSkewWindow(t *testing.T) {
	req := func() *http.Request {
		return sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "")
	}
	if _, err := verifierAt(signedAt.Add(14 * time.Minute)).Verify(req()); err != nil {
		t.Fatalf("14 minutes late: %v", err)
	}
	if _, err := verifierAt(signedAt.Add(-14 * time.Minute)).Verify(req()); err != nil {
		t.Fatalf("14 minutes early: %v", err)
	}
	_, err := verifierAt(signedAt.Add(16 * time.Minute)).Verify(req())
	wantCode(t, err, "RequestTimeTooSkewed")
	_, err = verifierAt(signedAt.Add(-16 * time.Minute)).Verify(req())
	wantCode(t, err, "RequestTimeTooSkewed")
}

func TestVerify_UnknownKeyAndWrongSecretAnswerTheSame(t *testing.T) {
	req := sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "")
	unknown := &Verifier{Authenticate: SecretAuthenticator(lookupOf(map[string]string{})), Now: func() time.Time { return signedAt }}
	_, unknownErr := unknown.Verify(req)
	wrong := &Verifier{Authenticate: SecretAuthenticator(lookupOf(map[string]string{exampleKeyID: "not-the-secret"})), Now: func() time.Time { return signedAt }}
	_, wrongErr := wrong.Verify(sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, ""))
	wantCode(t, unknownErr, "AccessDenied")
	wantCode(t, wrongErr, "AccessDenied")
	if unknownErr.Error() != wrongErr.Error() {
		t.Fatalf("an unknown key (%v) must read exactly like a wrong secret (%v)", unknownErr, wrongErr)
	}
}

func TestVerify_LookupFailureIsUnavailableNotDenied(t *testing.T) {
	req := sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "")
	sealed := &Verifier{
		Authenticate: SecretAuthenticator(func(context.Context, string) (string, error) { return "", errVaultSealed }),
		Now:    func() time.Time { return signedAt },
	}
	_, err := sealed.Verify(req)
	wantCode(t, err, "ServiceUnavailable")
	if strings.Contains(err.Error(), "vault") {
		t.Fatalf("the client must not learn why: %v", err)
	}
}

func TestVerify_RefusesRequestsThatAreNotHeaderSigV4(t *testing.T) {
	cases := map[string]func(r *http.Request){
		"anonymous": func(r *http.Request) { r.Header.Del("Authorization") },
		"signature v2": func(r *http.Request) {
			r.Header.Set("Authorization", "AWS "+exampleKeyID+":c2lnbmF0dXJl")
		},
		"presigned query": func(r *http.Request) {
			r.Header.Del("Authorization")
			r.URL.RawQuery = "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=" + exampleKeyID + "%2F20261018%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Signature=00"
		},
		"another algorithm": func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256", "AWS4-ECDSA-P256-SHA256", 1))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			req := sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "")
			change(req)
			_, err := verifierAt(signedAt).Verify(req)
			wantCode(t, err, "AccessDenied")
		})
	}
}

func TestVerify_MalformedAuthorization(t *testing.T) {
	base := "AWS4-HMAC-SHA256 Credential=" + exampleKeyID + "/20261018/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" + strings.Repeat("0", 64)
	cases := map[string]string{
		"no signed headers":        strings.Replace(base, "SignedHeaders=host;x-amz-content-sha256;x-amz-date, ", "", 1),
		"host is not signed":       strings.Replace(base, "host;", "", 1),
		"payload hash not signed":  strings.Replace(base, "x-amz-content-sha256;", "", 1),
		"date is not signed":       strings.Replace(base, ";x-amz-date", "", 1),
		"another service":          strings.Replace(base, "/s3/", "/sts/", 1),
		"scope date is not today":  strings.Replace(base, "/20261018/", "/20261017/", 1),
		"short credential":         strings.Replace(base, "/us-east-1/s3/aws4_request", "", 1),
		"wrong terminator":         strings.Replace(base, "aws4_request", "aws4_reply", 1),
		"signature is not hex":     strings.Replace(base, strings.Repeat("0", 64), strings.Repeat("z", 64), 1),
		"signature has wrong size": strings.Replace(base, strings.Repeat("0", 64), "00", 1),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://s3.example.test/photos/a.txt", nil)
			req.Header.Set("X-Amz-Date", "20261018T120000Z")
			req.Header.Set("X-Amz-Content-Sha256", emptySHA256)
			req.Header.Set("Authorization", header)
			_, err := verifierAt(signedAt).Verify(req)
			wantCode(t, err, "AuthorizationHeaderMalformed")
		})
	}
}

func TestVerify_PayloadModes(t *testing.T) {
	cases := map[string]PayloadMode{
		unsignedPayload:                 PayloadUnsigned,
		streamingSignedPayload:          PayloadStreamingSigned,
		streamingSignedPayloadTrailer:   PayloadStreamingSignedTrailer,
		streamingUnsignedPayloadTrailer: PayloadStreamingUnsignedTrailer,
		emptySHA256:                     PayloadSigned,
	}
	for hash, want := range cases {
		req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, hash)
		auth, err := verifierAt(signedAt).Verify(req)
		if err != nil {
			t.Fatalf("%s: %v", hash, err)
		}
		if auth.Payload != want {
			t.Fatalf("%s: payload = %v, want %v", hash, auth.Payload, want)
		}
	}
	req := sdkSigned(t, http.MethodPut, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "not-a-hash")
	_, err := verifierAt(signedAt).Verify(req)
	wantCode(t, err, "InvalidArgument")
}

func TestVerify_MissingPayloadHash(t *testing.T) {
	req := sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, "")
	req.Header.Del("X-Amz-Content-Sha256")
	_, err := verifierAt(signedAt).Verify(req)
	wantCode(t, err, "InvalidRequest")
}

// flipLastHex changes the signature's last hex digit, keeping its length.
func flipLastHex(header string) string {
	last := header[len(header)-1]
	replacement := byte('0')
	if last == '0' {
		replacement = '1'
	}
	return header[:len(header)-1] + string(replacement)
}

// The gateway hands the canonical request to the control plane, which holds
// the secret; what it checks must be exactly what the gateway would have.
func TestCheck_RoundTripsThroughARemoteAuthenticator(t *testing.T) {
	var seen Check
	remote := func(_ context.Context, check Check) (*Grant, error) {
		seen = check
		key, ok := VerifyWithSecret(exampleSecret, check)
		if !ok {
			return nil, ErrDenied
		}
		return &Grant{SigningKey: key, Principal: "key-row"}, nil
	}
	verifier := &Verifier{Authenticate: remote, Now: func() time.Time { return signedAt }}
	req := sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a%20b.txt?x-id=GetObject", nil, nil, signedAt, "")
	auth, err := verifier.Verify(req)
	if err != nil {
		t.Fatal(err)
	}
	if auth.Principal != "key-row" || seen.AccessKeyID != exampleKeyID || seen.Region != "us-east-1" {
		t.Fatalf("auth = %+v, check = %+v", auth, seen)
	}
	method, path, query, err := seen.Request()
	if err != nil || method != http.MethodGet || path != "/photos/a b.txt" || query.Get("x-id") != "GetObject" {
		t.Fatalf("request = %s %q %v %v", method, path, query, err)
	}

	tampered := seen
	tampered.CanonicalRequest = strings.Replace(seen.CanonicalRequest, "/photos/", "/other/", 1)
	if _, ok := VerifyWithSecret(exampleSecret, tampered); ok {
		t.Fatal("a canonical request the client did not sign must not verify")
	}
	_, err = (&Verifier{Authenticate: func(context.Context, Check) (*Grant, error) { return nil, ErrDenied },
		Now: func() time.Time { return signedAt }}).Verify(sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, ""))
	wantCode(t, err, "AccessDenied")
	_, err = (&Verifier{Authenticate: func(context.Context, Check) (*Grant, error) { return nil, &Error{Code: "AccessDenied", Message: "the key may not write to photos", Status: 403} },
		Now: func() time.Time { return signedAt }}).Verify(sdkSigned(t, http.MethodGet, "http://s3.example.test/photos/a.txt", nil, nil, signedAt, ""))
	if err == nil || !strings.Contains(err.Error(), "may not write") {
		t.Fatalf("an authenticator's own S3 error passes through: %v", err)
	}
}

func TestCheck_RequestRefusesAMalformedCanonicalRequest(t *testing.T) {
	for _, canonical := range []string{"", "GET", "GET\n%zz\n\n", "GET\n/a\nx=%zz\n"} {
		check := Check{CanonicalRequest: strings.ReplaceAll(canonical, "\\n", "\n")}
		if _, _, _, err := check.Request(); err == nil {
			t.Errorf("%q parsed", canonical)
		}
	}
}
