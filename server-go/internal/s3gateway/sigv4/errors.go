// Package sigv4 verifies AWS Signature Version 4 on S3 requests and decodes
// their payloads (plain, aws-chunked signed, aws-chunked with a checksum
// trailer), checking every digest the client declared. It knows nothing about
// projects or buckets: the gateway asks it who signed a request and reads the
// body through it.
package sigv4

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrUnknownKey is what a SecretLookup returns for an access key it does not
// hold (never issued, revoked or expired). Any other lookup error means the
// lookup itself failed, and the request is answered as unavailable.
var ErrUnknownKey = errors.New("sigv4: unknown access key")

// Error is an S3 error: the code and status a client acts on and a message
// that is safe to show it.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func s3Error(status int, code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Status: status}
}

// errDenied is the one answer for an unknown key and a wrong signature, so a
// caller cannot tell which access key ids exist.
func errDenied() *Error {
	return s3Error(http.StatusForbidden, "AccessDenied",
		"the access key is not valid or the request signature does not match")
}

func errMalformed(format string, args ...any) *Error {
	return s3Error(http.StatusBadRequest, "AuthorizationHeaderMalformed", format, args...)
}

func errInvalidRequest(format string, args ...any) *Error {
	return s3Error(http.StatusBadRequest, "InvalidRequest", format, args...)
}

func errIncomplete() *Error {
	return s3Error(http.StatusBadRequest, "IncompleteBody",
		"the request body is shorter than its declared length")
}

func errBadDigest(name string) *Error {
	return s3Error(http.StatusBadRequest, "BadDigest", "the %s you specified did not match the body", name)
}
