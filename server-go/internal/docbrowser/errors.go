package docbrowser

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrInvalid          = errors.New("invalid request")
	ErrDocumentNotFound = errors.New("document not found")
	ErrProjectNotFound  = errors.New("project not found")
	ErrNotDocumentDB    = errors.New("this project was not created with DocumentDB")
	ErrNotServable      = errors.New("the project's database is not running")
	ErrUnavailable      = errors.New("the document database did not answer")
	ErrTimeout          = errors.New("the document database did not answer in time")
)

// QueryError is the gateway refusing what was asked of it. Its message is
// about the caller's own query, so it is safe and useful to show.
type QueryError struct {
	Message  string
	Conflict bool
}

func (e *QueryError) Error() string { return e.Message }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// classify turns a driver error into one the API can answer with. Anything
// that is not the server's verdict on the query is reported without detail,
// since a network error names the gateway's internal address.
func classify(err error) error {
	if err == nil || errors.Is(err, ErrDocumentNotFound) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) || mongo.IsTimeout(err) {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	if mongo.IsDuplicateKeyError(err) {
		return &QueryError{Message: serverMessage(err), Conflict: true}
	}
	var serverErr mongo.ServerError
	if errors.As(err, &serverErr) {
		return &QueryError{Message: serverMessage(err)}
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

func serverMessage(err error) string {
	var cmd mongo.CommandError
	if errors.As(err, &cmd) {
		return cmd.Message
	}
	var write mongo.WriteException
	if errors.As(err, &write) && len(write.WriteErrors) > 0 {
		return write.WriteErrors[0].Message
	}
	return err.Error()
}

// PublicMessage is what a caller may be told about err.
func PublicMessage(err error) string {
	var queryErr *QueryError
	if errors.As(err, &queryErr) {
		return queryErr.Message
	}
	for _, known := range []error{ErrTimeout, ErrUnavailable, ErrGatewayNotReady, ErrNotServable, ErrNotDocumentDB, ErrProjectNotFound, ErrDocumentNotFound} {
		if errors.Is(err, known) {
			return known.Error()
		}
	}
	if errors.Is(err, ErrInvalid) {
		return err.Error()
	}
	return "the document browser failed"
}
