package mcpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Built at run time: a literal id of this shape looks like a secret to a scanner.
var (
	s3KeyOne  = "s3k_" + strings.Repeat("1", 32)
	s3KeyTwo  = "s3k_" + strings.Repeat("2", 32)
	s3KeyNone = "s3k_" + strings.Repeat("0", 32)
)

// destructiveCase is one tool that needs a second call, with a second target
// of the same kind a token for the first must not reach.
type destructiveCase struct {
	tool      string
	args      map[string]any
	otherArgs map[string]any
	// missingArgs name a target that does not exist.
	missingArgs map[string]any
	// deletes is the request that destroys the target.
	deletes string
	// named is a word the description of the target must carry.
	named string
	setup func(f *fakeRoutes)
}

func destructiveCases() []destructiveCase {
	withProject := func(args map[string]any) map[string]any {
		args["project_id"] = testProjectA
		return args
	}
	return []destructiveCase{
		{
			tool: "delete_app", args: withProject(map[string]any{"app_id": "web"}), otherArgs: withProject(map[string]any{"app_id": "api"}), missingArgs: withProject(map[string]any{"app_id": "gone"}),
			deletes: "DELETE " + projectsA + "/apps/web/", named: "storefront",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/apps/web/", 200, `{"id":"web","name":"storefront","image":"nginx:1","port":80,"replicas":2,"status":"RUNNING","version":1}`)
				f.on(http.MethodGet, projectsA+"/apps/api/", 200, `{"id":"api","name":"backend","image":"api:1","port":80,"replicas":1,"status":"RUNNING","version":1}`)
				f.on(http.MethodDelete, projectsA+"/apps/web/", 204, ``)
				f.on(http.MethodDelete, projectsA+"/apps/api/", 204, ``)
			},
		},
		{
			tool: "delete_function", args: withProject(map[string]any{"id": "shorten"}), otherArgs: withProject(map[string]any{"id": "resize"}), missingArgs: withProject(map[string]any{"id": "gone"}),
			deletes: "DELETE " + projectsA + "/functions/shorten/", named: "shorten",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/functions/", 200, `[{"id":"shorten","name":"Link shortener","version":3,"active":true},{"id":"resize","name":"Image resize","version":1,"active":true}]`)
				f.on(http.MethodDelete, projectsA+"/functions/shorten/", 204, ``)
				f.on(http.MethodDelete, projectsA+"/functions/resize/", 204, ``)
			},
		},
		{
			tool: "revoke_publishable_key", args: withProject(map[string]any{"key_id": 7}), otherArgs: withProject(map[string]any{"key_id": 9}), missingArgs: withProject(map[string]any{"key_id": 12345}),
			deletes: "DELETE " + projectsA + "/sdk-keys/7", named: "esk_pub_ab12",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sdk-keys/", 200, `{"keys":[{"id":7,"keyPrefix":"esk_pub_ab12","keyType":"publishable","name":"web"},{"id":9,"keyPrefix":"esk_pub_ef56","keyType":"publishable","name":"docs"}]}`)
				f.on(http.MethodDelete, projectsA+"/sdk-keys/7", 204, ``)
				f.on(http.MethodDelete, projectsA+"/sdk-keys/9", 204, ``)
			},
		},
		{
			tool: "delete_site", args: withProject(map[string]any{"site_id": "s1"}), otherArgs: withProject(map[string]any{"site_id": "s2"}), missingArgs: withProject(map[string]any{"site_id": "gone"}),
			deletes: "DELETE " + projectsA + "/sites/s1/", named: "shop",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/sites/s1/", 200, `{"id":"s1","name":"shop","hostname":"shop.example.test"}`)
				f.on(http.MethodGet, projectsA+"/sites/s2/", 200, `{"id":"s2","name":"docs","hostname":"docs.example.test"}`)
				f.on(http.MethodDelete, projectsA+"/sites/s1/", 204, ``)
				f.on(http.MethodDelete, projectsA+"/sites/s2/", 204, ``)
			},
		},
		{
			tool: "delete_function_secret", args: withProject(map[string]any{"key": "STRIPE_KEY"}), otherArgs: withProject(map[string]any{"key": "MAIL_KEY"}), missingArgs: withProject(map[string]any{"key": "GONE_KEY"}),
			deletes: "DELETE " + projectsA + "/functions/secrets/STRIPE_KEY", named: "STRIPE_KEY",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/functions/secrets", 200, `[{"key":"STRIPE_KEY"},{"key":"MAIL_KEY"}]`)
				f.on(http.MethodDelete, projectsA+"/functions/secrets/STRIPE_KEY", 204, ``)
				f.on(http.MethodDelete, projectsA+"/functions/secrets/MAIL_KEY", 204, ``)
				f.on(http.MethodGet, projectsA+"/functions/runtime/status", 200, `{"status":"running","healthy":true}`)
			},
		},
		{
			tool: "revoke_s3_key", args: withProject(map[string]any{"key_id": s3KeyOne}), otherArgs: withProject(map[string]any{"key_id": s3KeyTwo}), missingArgs: withProject(map[string]any{"key_id": s3KeyNone}),
			deletes: "DELETE " + projectsA + "/storage/s3-keys/" + s3KeyOne, named: "ci uploads",
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/storage/s3-keys", 200,
					`{"keys":[{"id":"`+s3KeyOne+`","name":"ci uploads","accessKeyId":"AKIA1","buckets":["files"]},{"id":"`+s3KeyTwo+`","name":"backups","accessKeyId":"AKIA2","buckets":[]}],"endpoint":"https://s3.example.test","region":"auto"}`)
				f.on(http.MethodDelete, projectsA+"/storage/s3-keys/"+s3KeyOne, 204, ``)
				f.on(http.MethodDelete, projectsA+"/storage/s3-keys/"+s3KeyTwo, 204, ``)
			},
		},
	}
}

// harness is one platform process: one environment, so tokens issued on one
// connection are what another connection of the same process redeems.
type harness struct {
	t      *testing.T
	routes *fakeRoutes
	env    *env
	clock  *fakeClock
	audit  *recordingAudit
}

func newHarness(t *testing.T, c destructiveCase, on ...features.Feature) *harness {
	t.Helper()
	routes := newFakeRoutes()
	c.setup(routes)
	audit := &recordingAudit{}
	settings := testSettings
	settings.Features = features.NewStatic(append([]features.Feature{features.S3}, on...)...)
	clock := &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
	e := newEnv(routes, audit, settings)
	e.confirms.now = clock.read
	return &harness{t: t, routes: routes, env: e, clock: clock, audit: audit}
}

func (h *harness) connect(caller Caller) *mcp.ClientSession {
	h.t.Helper()
	ctx := context.Background()
	server := newServer(ctx, h.env, caller)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		h.t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		h.t.Fatalf("client connect: %v", err)
	}
	h.t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func (h *harness) deleteCount(call string) int {
	n := 0
	for _, made := range h.routes.calls {
		if made.Method+" "+made.Path == call {
			n++
		}
	}
	return n
}

func (h *harness) anyDelete() bool {
	for _, made := range h.routes.calls {
		if made.Method == http.MethodDelete {
			return true
		}
	}
	return false
}

func withToken(args map[string]any, token string) map[string]any {
	out := map[string]any{"confirmation_token": token}
	for key, value := range args {
		out[key] = value
	}
	return out
}

func requestToken(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	out := structured(t, callTool(t, cs, tool, args))
	token, _ := out["confirmationToken"].(string)
	if token == "" {
		t.Fatalf("%s gave no confirmation token: %v", tool, out)
	}
	return token
}

func TestFirstCallDeletesNothingAndAsksForConfirmation(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			out := structured(t, callTool(t, h.connect(writeCaller()), c.tool, c.args))
			if h.anyDelete() {
				t.Fatal("the first call deleted something")
			}
			assertAsksForConfirmation(t, out, c.named)
		})
	}
}

func assertAsksForConfirmation(t *testing.T, out map[string]any, named string) {
	t.Helper()
	if out["confirmationRequired"] != true {
		t.Errorf("confirmationRequired = %v", out["confirmationRequired"])
	}
	if token, _ := out["confirmationToken"].(string); token == "" {
		t.Error("no confirmation token")
	}
	if seconds, _ := out["expiresInSeconds"].(float64); seconds <= 0 || seconds > 300 {
		t.Errorf("expiresInSeconds = %v, want within five minutes", out["expiresInSeconds"])
	}
	if !strings.Contains(realJSON(out, nil), named) {
		t.Errorf("the description does not name the target %q: %v", named, out)
	}
	if _, deleted := out["deleted"]; deleted {
		t.Error("the answer says something was deleted")
	}
}

func TestSecondCallWithTheTokenDeletes(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			cs := h.connect(writeCaller())
			token := requestToken(t, cs, c.tool, c.args)
			res := callTool(t, cs, c.tool, withToken(c.args, token))
			if res.IsError {
				t.Fatalf("confirmed call failed: %s", resultText(res))
			}
			if got := h.deleteCount(c.deletes); got != 1 {
				t.Fatalf("%s made %d times, want 1", c.deletes, got)
			}
		})
	}
}

func TestAConfirmedCallWorksOnAnotherConnectionOfTheSameCaller(t *testing.T) {
	c := destructiveCases()[0]
	h := newHarness(t, c, features.MCPConfirm)
	token := requestToken(t, h.connect(writeCaller()), c.tool, c.args)
	if res := callTool(t, h.connect(writeCaller()), c.tool, withToken(c.args, token)); res.IsError {
		t.Fatalf("a stateless reconnect lost the token: %s", resultText(res))
	}
	if h.deleteCount(c.deletes) != 1 {
		t.Fatal("not deleted")
	}
}

func TestAReplayedTokenIsRefused(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			cs := h.connect(writeCaller())
			token := requestToken(t, cs, c.tool, c.args)
			callTool(t, cs, c.tool, withToken(c.args, token))
			res := callTool(t, cs, c.tool, withToken(c.args, token))
			if !res.IsError {
				t.Fatal("the same token confirmed twice")
			}
			if got := h.deleteCount(c.deletes); got != 1 {
				t.Fatalf("%s made %d times, want 1", c.deletes, got)
			}
		})
	}
}

func TestATokenForAnotherTargetIsRefused(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			cs := h.connect(writeCaller())
			token := requestToken(t, cs, c.tool, c.args)
			res := callTool(t, cs, c.tool, withToken(c.otherArgs, token))
			if !res.IsError {
				t.Fatal("a token for one target confirmed another")
			}
			if h.anyDelete() {
				t.Fatal("something was deleted on a token for another target")
			}
		})
	}
}

func TestATokenForAnotherToolIsRefused(t *testing.T) {
	cases := destructiveCases()
	h := newHarness(t, cases[0], features.MCPConfirm)
	for _, c := range cases[1:] {
		c.setup(h.routes)
	}
	cs := h.connect(writeCaller())
	token := requestToken(t, cs, cases[0].tool, cases[0].args)
	for _, c := range cases[1:] {
		if res := callTool(t, cs, c.tool, withToken(c.args, token)); !res.IsError {
			t.Errorf("a delete_app token confirmed %s", c.tool)
		}
	}
	if h.anyDelete() {
		t.Fatal("something was deleted on a token for another tool")
	}
}

func TestATokenForAnotherProjectIsRefused(t *testing.T) {
	c := destructiveCases()[0]
	h := newHarness(t, c, features.MCPConfirm)
	h.routes.on(http.MethodGet, "/api/projects/proj-b/apps/web/", 200, `{"id":"web","name":"storefront","image":"nginx:1","port":80,"replicas":1,"status":"RUNNING","version":1}`)
	h.routes.on(http.MethodDelete, "/api/projects/proj-b/apps/web/", 204, ``)
	cs := h.connect(writeCaller())
	token := requestToken(t, cs, c.tool, c.args)
	args := withToken(c.args, token)
	args["project_id"] = "proj-b"
	if res := callTool(t, cs, c.tool, args); !res.IsError {
		t.Fatal("a token for one project confirmed the same app id in another")
	}
	if h.anyDelete() {
		t.Fatal("deleted in another project")
	}
}

func TestAnExpiredTokenIsRefused(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			cs := h.connect(writeCaller())
			token := requestToken(t, cs, c.tool, c.args)
			h.clock.now = h.clock.now.Add(confirmTTL + time.Second)
			res := callTool(t, cs, c.tool, withToken(c.args, token))
			if !res.IsError || !strings.Contains(resultText(res), "expired") {
				t.Fatalf("result = %s", resultText(res))
			}
			if h.anyDelete() {
				t.Fatal("deleted on an expired token")
			}
		})
	}
}

func TestATokenOfAnotherCallerIsRefused(t *testing.T) {
	other := writeCaller()
	other.User = userNamed("user-2")
	other.Token = tokenNamed("hash-2")
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			token := requestToken(t, h.connect(writeCaller()), c.tool, c.args)
			res := callTool(t, h.connect(other), c.tool, withToken(c.args, token))
			if !res.IsError {
				t.Fatal("another caller confirmed with a token it was not given")
			}
			if h.anyDelete() {
				t.Fatal("deleted for another caller")
			}
		})
	}
}

func TestAGarbageTokenIsRefused(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			cs := h.connect(writeCaller())
			for _, token := range []string{"nope", "mcpc_", strings.Repeat("a", 500)} {
				if res := callTool(t, cs, c.tool, withToken(c.args, token)); !res.IsError {
					t.Errorf("token %.10q accepted", token)
				}
			}
			if h.anyDelete() {
				t.Fatal("deleted on a garbage token")
			}
		})
	}
}

// A target that is not there is refused at the first call, so no token is
// issued for something that cannot be deleted.
func TestNoTokenIsIssuedForATargetThatIsNotThere(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c, features.MCPConfirm)
			res := callTool(t, h.connect(writeCaller()), c.tool, c.missingArgs)
			if !res.IsError {
				t.Fatalf("a missing target got an answer: %v", res.StructuredContent)
			}
			if h.anyDelete() {
				t.Fatal("deleted")
			}
		})
	}
}

func TestTheSwitchedOffGuardDeletesAtOnce(t *testing.T) {
	for _, c := range destructiveCases() {
		t.Run(c.tool, func(t *testing.T) {
			h := newHarness(t, c)
			res := callTool(t, h.connect(writeCaller()), c.tool, c.args)
			if res.IsError {
				t.Fatalf("failed: %s", resultText(res))
			}
			if h.deleteCount(c.deletes) != 1 {
				t.Fatal("the call did not delete with the guard off")
			}
		})
	}
}

func TestTheAuditTrailSaysWhichCallAskedAndWhichConfirmed(t *testing.T) {
	c := destructiveCases()[0]
	h := newHarness(t, c, features.MCPConfirm)
	cs := h.connect(writeCaller())
	token := requestToken(t, cs, c.tool, c.args)
	callTool(t, cs, c.tool, withToken(c.args, token))
	if len(h.audit.entries) != 2 {
		t.Fatalf("%d audit entries, want 2", len(h.audit.entries))
	}
	if !strings.Contains(h.audit.entries[0].Details, `"confirmation":"requested"`) {
		t.Errorf("first entry: %s", h.audit.entries[0].Details)
	}
	if !strings.Contains(h.audit.entries[1].Details, `"confirmation":"confirmed"`) {
		t.Errorf("second entry: %s", h.audit.entries[1].Details)
	}
	for _, entry := range h.audit.entries {
		if strings.Contains(entry.Details, token) {
			t.Error("the token reached the audit log")
		}
	}
}

func TestTheConfirmationArgumentIsDocumentedOnEveryGuardedTool(t *testing.T) {
	h := newHarness(t, destructiveCases()[0], features.MCPConfirm)
	listed, err := h.connect(writeCaller()).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	guarded := map[string]bool{}
	for _, c := range destructiveCases() {
		guarded[c.tool] = true
	}
	for _, tool := range listed.Tools {
		if !guarded[tool.Name] {
			continue
		}
		if !strings.Contains(realJSON(tool.InputSchema, nil), "confirmation_token") {
			t.Errorf("%s does not take confirmation_token", tool.Name)
		}
		delete(guarded, tool.Name)
	}
	for name := range guarded {
		t.Errorf("%s is not offered", name)
	}
}

func TestEveryGuardedToolStaysAWriteTool(t *testing.T) {
	h := newHarness(t, destructiveCases()[0], features.MCPConfirm)
	listed, err := h.connect(readOnlyCaller()).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		for _, c := range destructiveCases() {
			if tool.Name == c.tool {
				t.Errorf("%s is offered on a read-only connection", c.tool)
			}
		}
	}
}
