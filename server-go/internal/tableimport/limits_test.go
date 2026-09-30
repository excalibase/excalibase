package tableimport

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestForTier_EveryPlanHasLimitsAndWorkbooksStaySmall(t *testing.T) {
	for _, tier := range []domain.TierType{domain.Free, domain.Standard, domain.Enterprise} {
		lim, err := ForTier(tier)
		if err != nil || lim.MaxBytes == 0 || lim.MaxRows == 0 || lim.MaxXLSXBytes > lim.MaxBytes {
			t.Errorf("%s: %+v %v", tier, lim, err)
		}
	}
	if free, _ := ForTier(domain.Free); free.MaxBytes != 50*mib {
		t.Errorf("FREE cap = %d", free.MaxBytes)
	}
	if _, err := ForTier("GOLD"); err == nil {
		t.Error("an unknown plan got limits")
	}
}

func TestErrorMessages(t *testing.T) {
	if (&RowErrors{}).Error() == "" {
		t.Error("empty RowErrors message")
	}
	got := (&RowErrors{Errors: []RowError{{Line: 4, Message: "bad"}}}).Error()
	if !strings.Contains(got, "line 4") {
		t.Errorf("got %q", got)
	}
	if (&FileError{Msg: "whole file"}).Error() != "whole file" {
		t.Error("a file error without a line changed its message")
	}
}

func TestSanitiseLibError_NeverShowsAPath(t *testing.T) {
	if got := sanitiseLibError(errors.New("open /tmp/excalibase-import-1: denied")); strings.Contains(got, "/tmp") {
		t.Errorf("got %q", got)
	}
	if got := sanitiseLibError(errors.New("bad cell")); got != "bad cell" {
		t.Errorf("got %q", got)
	}
}

func TestNewSheetsFetcher_ReachesOnlyDocsGoogle(t *testing.T) {
	f := NewSheetsFetcher()
	if f.base.String() != "https://docs.google.com" || f.client.CheckRedirect == nil || f.client.Timeout == 0 {
		t.Fatalf("fetcher = %+v", f)
	}
	inner := errors.New("dial refused")
	if unwrapURLError(&url.Error{Op: "Get", URL: "https://docs.google.com", Err: inner}) != inner {
		t.Error("the URL wrapper was kept")
	}
}
