package mcpserver

import (
	"strings"
	"testing"
)

// EXC-641: a qualified table naming another schema than schema is refused.
func TestSetRealtimeRefusesTwoDifferentSchemas(t *testing.T) {
	cs := session(t, newFakeRoutes(), &recordingAudit{}, writeCaller())
	result := callTool(t, cs, "set_realtime", map[string]any{"project_id": testProjectA, "schema": "public", "table": "app.todos", "enabled": true})
	if !result.IsError || !strings.Contains(resultText(result), "schema") {
		t.Fatalf("result = %+v", result)
	}
}
