package cfworkers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"testing"
)

// filePart is one multipart part with the filename it was sent under.
type filePart struct{ name, filename, contentType, content string }

func readFileParts(t *testing.T, r *http.Request, body []byte) []filePart {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	var parts []filePart
	for {
		part, err := reader.NextPart()
		if err != nil {
			return parts
		}
		content, _ := io.ReadAll(part)
		// FileName drops folders, so the raw parameter is read.
		_, disposition, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		parts = append(parts, filePart{name: part.FormName(), filename: disposition["filename"], contentType: part.Header.Get("Content-Type"), content: string(content)})
	}
}

func staticVersion() ScriptVersion {
	return ScriptVersion{AssetsJWT: "completion-jwt", Assets: AssetConfig{HTMLHandling: HTMLAutoTrailingSlash, NotFoundHandling: NotFoundSinglePage}}
}

func module(name, contentType, body string) Module {
	return Module{Name: name, ContentType: contentType, Open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(body)), nil }}
}

func serverVersion() ScriptVersion {
	return ScriptVersion{
		AssetsJWT:          "completion-jwt",
		Assets:             AssetConfig{HTMLHandling: HTMLAutoTrailingSlash, NotFoundHandling: NotFoundNone},
		MainModule:         "worker.js",
		CompatibilityDate:  "2025-09-01",
		CompatibilityFlags: []string{"nodejs_compat"},
		Modules: []Module{
			module("worker.js", ModuleJavaScript, "export default {}"),
			module("chunks/a.js", ModuleJavaScript, "export const a = 1"),
			module("worker.js.map", ModuleSourceMap, "{}"),
		},
		Bindings: []Binding{{Type: BindingPlainText, Name: "EXCALIBASE_URL", Text: "https://api.example"}},
		Tags:     []string{"site-abc", "project-p1"},
	}
}

// The assets-only metadata static sites have always been published with, byte for byte.
func TestPutScriptKeepsTheStaticMetadataExactly(t *testing.T) {
	var parts []filePart
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		parts = readFileParts(t, r, body)
		writeEnvelope(w, http.StatusOK, nil)
	})
	if err := client.PutScript(context.Background(), testScript, staticVersion()); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	want := `{"compatibility_date":"2025-09-01","assets":{"jwt":"completion-jwt","config":{"html_handling":"auto-trailing-slash","not_found_handling":"single-page-application"}}}`
	if len(parts) != 1 || parts[0].content != want {
		t.Fatalf("parts = %+v\nwant only metadata %s", parts, want)
	}
}

func TestPutScriptPublishesServerModulesWithOurBindings(t *testing.T) {
	var parts []filePart
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		parts = readFileParts(t, r, body)
		writeEnvelope(w, http.StatusOK, nil)
	})
	if err := client.PutScript(context.Background(), testScript, serverVersion()); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	if len(parts) != 4 || parts[0].name != "metadata" {
		t.Fatalf("parts = %+v", parts)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(parts[0].content), &metadata); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if metadata["main_module"] != "worker.js" || metadata["compatibility_date"] != "2025-09-01" {
		t.Errorf("metadata = %v", metadata)
	}
	if flags, _ := json.Marshal(metadata["compatibility_flags"]); string(flags) != `["nodejs_compat"]` {
		t.Errorf("flags = %s", flags)
	}
	// Every publish states all its bindings, so an older version never inherits a newer one.
	if _, kept := metadata["keep_bindings"]; kept {
		t.Error("metadata asks Cloudflare to keep bindings")
	}
	bindings, _ := json.Marshal(metadata["bindings"])
	if string(bindings) != `[{"name":"ASSETS","type":"assets"},{"name":"EXCALIBASE_URL","text":"https://api.example","type":"plain_text"}]` {
		t.Errorf("bindings = %s", bindings)
	}
	assets, _ := metadata["assets"].(map[string]any)
	config, _ := assets["config"].(map[string]any)
	if assets["jwt"] != "completion-jwt" || config["not_found_handling"] != "none" {
		t.Errorf("assets = %v", assets)
	}
	if tags, _ := json.Marshal(metadata["tags"]); string(tags) != `["site-abc","project-p1"]` {
		t.Errorf("tags = %s", tags)
	}
	want := []filePart{
		{"worker.js", "worker.js", "application/javascript+module", "export default {}"},
		{"chunks/a.js", "chunks/a.js", "application/javascript+module", "export const a = 1"},
		{"worker.js.map", "worker.js.map", "application/source-map", "{}"},
	}
	for i, part := range want {
		if parts[i+1] != part {
			t.Errorf("part %d = %+v, want %+v", i+1, parts[i+1], part)
		}
	}
}

func TestPutScriptSendsScriptLimitsOnlyWhenSet(t *testing.T) {
	var metadata map[string]any
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_ = json.Unmarshal([]byte(readFileParts(t, r, body)[0].content), &metadata)
		writeEnvelope(w, http.StatusOK, nil)
	})
	version := serverVersion()
	version.Limits = &ScriptLimits{CPUMillis: 50, Subrequests: 20}
	if err := client.PutScript(context.Background(), testScript, version); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	if limits, _ := json.Marshal(metadata["limits"]); string(limits) != `{"cpu_ms":50,"subrequests":20}` {
		t.Errorf("limits = %s", limits)
	}
}

func TestPutScriptRefusesAVersionCloudflareShouldNeverSee(t *testing.T) {
	cases := map[string]func(*ScriptVersion){
		"main module not uploaded": func(v *ScriptVersion) { v.MainModule = "missing.js" },
		"main module is a map":     func(v *ScriptVersion) { v.MainModule = "worker.js.map" },
		"modules without a main":   func(v *ScriptVersion) { v.MainModule = "" },
		"no compatibility date":    func(v *ScriptVersion) { v.CompatibilityDate = "" },
		"service binding": func(v *ScriptVersion) {
			v.Bindings = append(v.Bindings, Binding{Type: "service", Name: "WORKER_SELF_REFERENCE"})
		},
		"kv binding":     func(v *ScriptVersion) { v.Bindings = append(v.Bindings, Binding{Type: "kv_namespace", Name: "KV"}) },
		"assets binding": func(v *ScriptVersion) { v.Bindings = append(v.Bindings, Binding{Type: "assets", Name: "OTHER"}) },
		"binding named ASSETS": func(v *ScriptVersion) {
			v.Bindings = append(v.Bindings, Binding{Type: BindingPlainText, Name: "ASSETS"})
		},
		"duplicate binding": func(v *ScriptVersion) {
			v.Bindings = append(v.Bindings, Binding{Type: BindingSecretText, Name: "EXCALIBASE_URL", Text: "x"})
		},
		"binding without a name": func(v *ScriptVersion) { v.Bindings = append(v.Bindings, Binding{Type: BindingPlainText}) },
		"duplicate module":       func(v *ScriptVersion) { v.Modules = append(v.Modules, module("worker.js", ModuleJavaScript, "")) },
		"module named metadata":  func(v *ScriptVersion) { v.Modules = append(v.Modules, module("metadata", ModuleText, "")) },
		"module climbing out":    func(v *ScriptVersion) { v.Modules = append(v.Modules, module("../x.js", ModuleJavaScript, "")) },
		"absolute module":        func(v *ScriptVersion) { v.Modules = append(v.Modules, module("/x.js", ModuleJavaScript, "")) },
		"module with a quote":    func(v *ScriptVersion) { v.Modules = append(v.Modules, module(`a".js`, ModuleJavaScript, "")) },
		"unknown module type":    func(v *ScriptVersion) { v.Modules = append(v.Modules, module("x.py", "text/x-python", "")) },
		"bindings on a static version": func(v *ScriptVersion) {
			*v = staticVersion()
			v.Bindings = []Binding{{Type: BindingPlainText, Name: "A", Text: "b"}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			fake, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeEnvelope(w, http.StatusOK, nil) })
			version := serverVersion()
			change(&version)
			if err := client.PutScript(context.Background(), testScript, version); err == nil {
				t.Fatal("want the version refused")
			}
			if len(fake.requests) != 0 {
				t.Fatalf("requests = %d, want none", len(fake.requests))
			}
		})
	}
}

// A secret's value travels only inside the metadata part, never in an error.
func TestABindingNeverFormatsItsText(t *testing.T) {
	binding := Binding{Type: BindingSecretText, Name: "API_KEY", Text: "s3cr3t-value"}
	for _, formatted := range []string{fmt.Sprintf("%v", binding), fmt.Sprintf("%+v", binding), fmt.Sprintf("%#v", binding), fmt.Sprint([]Binding{binding})} {
		if strings.Contains(formatted, "s3cr3t-value") {
			t.Fatalf("formatted binding shows its text: %s", formatted)
		}
	}
}

// wrangler's own namespace upload, captured against a local fake API (EXC-661 spike):
// every field we send is one wrangler sends, except keep_bindings, which we never send.
func TestPutScriptSpeaksTheUploadWranglerSends(t *testing.T) {
	raw, err := os.ReadFile("testdata/wrangler-namespace-put.json")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var wrangler struct {
		Metadata map[string]any `json:"metadata"`
		Parts    []struct{ Name, Filename, Type string }
	}
	if err := json.Unmarshal(raw, &wrangler); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var parts []filePart
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		parts = readFileParts(t, r, body)
		writeEnvelope(w, http.StatusOK, nil)
	})
	version := serverVersion()
	version.Tags = nil
	version.Modules = []Module{module("worker.js", ModuleJavaScript, "x"), module("worker.js.map", ModuleSourceMap, "{}")}
	if err := client.PutScript(context.Background(), testScript, version); err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	var ours map[string]any
	if err := json.Unmarshal([]byte(parts[0].content), &ours); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	for key := range ours {
		if _, ok := wrangler.Metadata[key]; !ok {
			t.Errorf("we send %q, which wrangler does not", key)
		}
	}
	if _, ok := wrangler.Metadata["keep_bindings"]; !ok {
		t.Fatal("fixture lost keep_bindings; the comparison above no longer proves we drop it")
	}
	for i, want := range wrangler.Parts {
		got := parts[i+1]
		if got.name != want.Name || got.filename != want.Filename || got.contentType != want.Type {
			t.Errorf("part %d = %+v, wrangler sends %+v", i+1, got, want)
		}
	}
}
