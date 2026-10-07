package handler

import (
	"net/http"
	"strings"
	"testing"
)

// EXC-560: an import the runtime does not carry needs the project's egress;
// without it the worker hung until "worker init timeout". The deploy refuses
// it by name, deploys nothing and keeps no record; once the host is allowed
// the same code deploys.
func TestCreate_RefusesImportsTheRuntimeCannotFetch(t *testing.T) {
	f := setupEgressHandler(t)
	body := map[string]interface{}{
		"id": "chunky", "name": "chunky",
		"files": []map[string]string{{"path": testIndexTS, "content": `import chunk from "npm:lodash.chunk@4.2.0";
export default (req: Request) => new Response(JSON.stringify(chunk([1, 2, 3], 2)));`}},
	}
	path := "/api/projects/" + testEgressProject + "/functions/"
	w := doJSON(f.router, "POST", path, body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("deploy: %d %s, want 400", w.Code, w.Body.String())
	}
	for _, want := range []string{"npm:lodash.chunk@4.2.0 (registry.npmjs.org)", "egress"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("refusal %q does not name %q", w.Body.String(), want)
		}
	}
	if len(*f.scripts) != 0 {
		t.Fatalf("runtime got a deploy: %v", *f.scripts)
	}
	if fn, _ := f.handler.store.Get(testEgressProject, "chunky"); fn != nil {
		t.Fatal("a refused deploy left a function record")
	}

	if w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {"registry.npmjs.org"}}); w.Code != http.StatusOK {
		t.Fatalf("PUT egress: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(f.router, "POST", path, body); w.Code != http.StatusCreated {
		t.Fatalf("deploy with the registry allowed: %d %s", w.Code, w.Body.String())
	}
}

// The shared docker runtime has no NetworkPolicy fencing its module loader,
// so its deploys are not checked.
func TestCreate_SharedRuntimeIsNotChecked(t *testing.T) {
	r, _, _, _ := setupFunctionHandler(t)
	body := map[string]interface{}{
		"id": "chunky", "name": "chunky",
		"files": []map[string]string{{"path": testIndexTS, "content": `import chunk from "npm:lodash.chunk@4.2.0";
export default (req: Request) => new Response(String(chunk([1], 1)));`}},
	}
	if w := doJSON(r, "POST", testProj1FnPath, body); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
}

func TestCreate_CarriedImportsNeedNoEgress(t *testing.T) {
	f := setupEgressHandler(t)
	body := map[string]interface{}{
		"id": "echo", "name": "echo",
		"files": []map[string]string{{"path": testIndexTS, "content": `import { z } from "zod";
import { query } from "@excalibase/server";
export default query({ args: z.object({}), handler: async () => "ok" });`}},
	}
	if w := doJSON(f.router, "POST", "/api/projects/"+testEgressProject+"/functions/", body); w.Code != http.StatusCreated {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
}
