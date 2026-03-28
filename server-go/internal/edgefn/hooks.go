package edgefn

import (
	"context"
	"fmt"
	"log"
	"time"
)

type HookContext struct {
	ProjectID    string `json:"projectId"`
	OrgID        string `json:"orgId,omitempty"`
	DatabaseType string `json:"databaseType,omitempty"`
	Tier         string `json:"tier,omitempty"`
	// Credentials (only for post-provision)
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Database string `json:"database,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type HookResult struct {
	ScriptID string      `json:"scriptId"`
	Name     string      `json:"name"`
	Success  bool        `json:"success"`
	Result   interface{} `json:"result,omitempty"`
	Error    string      `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

type HookService struct {
	store  *ScriptStore
	client *RuntimeClient
}

func NewHookService(store *ScriptStore, client *RuntimeClient) *HookService {
	return &HookService{store: store, client: client}
}

// ExecuteHooks runs all active scripts of the given hook type synchronously.
// Returns results for each script. Never panics — errors are captured per-script.
func (h *HookService) ExecuteHooks(ctx context.Context, hookType string, hookCtx HookContext) []HookResult {
	scripts, err := h.store.ListByHookType(hookType)
	if err != nil || len(scripts) == 0 {
		return nil
	}

	results := make([]HookResult, 0, len(scripts))

	for _, sc := range scripts {
		start := time.Now()

		// Ensure script is deployed to runtime
		if err := h.client.Deploy(ctx, sc.ID, sc.Code); err != nil {
			results = append(results, HookResult{
				ScriptID: sc.ID, Name: sc.Name, Success: false,
				Error: fmt.Sprintf("deploy failed: %v", err), Duration: time.Since(start),
			})
			continue
		}

		// Invoke
		result, err := h.client.Invoke(ctx, sc.ID, hookCtx)
		if err != nil {
			results = append(results, HookResult{
				ScriptID: sc.ID, Name: sc.Name, Success: false,
				Error: fmt.Sprintf("invoke failed: %v", err), Duration: time.Since(start),
			})
			continue
		}

		results = append(results, HookResult{
			ScriptID: sc.ID, Name: sc.Name, Success: true,
			Result: result, Duration: time.Since(start),
		})

		log.Printf("[hooks] %s/%s completed in %v", hookType, sc.Name, time.Since(start))
	}

	return results
}

// ExecuteHooksAsync runs hooks in a goroutine — non-blocking.
// Optional callback receives results when complete.
func (h *HookService) ExecuteHooksAsync(ctx context.Context, hookType string, hookCtx HookContext, callback func([]HookResult)) {
	go func() {
		results := h.ExecuteHooks(ctx, hookType, hookCtx)
		if callback != nil {
			callback(results)
		}
	}()
}
