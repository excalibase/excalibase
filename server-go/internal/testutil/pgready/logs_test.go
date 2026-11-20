package pgready

import (
	"context"
	"io"
	"strings"
	"testing"
)

type countingDumper struct{ reads int }

func (c *countingDumper) Logs(context.Context) (io.ReadCloser, error) {
	c.reads++
	return io.NopCloser(strings.NewReader("log line")), nil
}

func TestDumpLogsOnFailure_LeavesLogsUnreadWhenTheTestPasses(t *testing.T) {
	dumper := &countingDumper{}
	t.Run("passing", func(t *testing.T) { DumpLogsOnFailure(t, dumper) })
	if dumper.reads != 0 {
		t.Fatalf("logs read for a passing test: %d", dumper.reads)
	}
}
