package email

import (
	"context"
	"testing"
)

type realSender struct{}

func (realSender) Send(context.Context, Message) (string, error) { return "id", nil }

func TestConfigured(t *testing.T) {
	if Configured(nil) {
		t.Error("a nil sender is configured")
	}
	if Configured(NewNoopSender()) {
		t.Error("the noop sender is configured")
	}
	if !Configured(realSender{}) {
		t.Error("a real sender is not configured")
	}
}
