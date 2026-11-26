package credits

import (
	"context"
	"errors"
	"testing"
)

func TestRecordTermsStoresTheCurrentVersionWithoutGranting(t *testing.T) {
	ledger := &recordingLedger{terms: map[string]string{}}
	service := newTestService(ledger)
	if err := service.RecordTerms(context.Background(), "u1", "terms-1"); err != nil {
		t.Fatal(err)
	}
	if ledger.terms["u1"] != "terms-1" || len(ledger.grants) != 0 {
		t.Fatalf("terms %v grants %v: recording the box must not grant before the address is verified", ledger.terms, ledger.grants)
	}
	if err := service.RecordTerms(context.Background(), "u2", "old"); !errors.Is(err, ErrTermsVersion) || len(ledger.terms) != 1 {
		t.Fatalf("an old version: %v %v", err, ledger.terms)
	}
}
