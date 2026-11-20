//go:build integration

package k8s

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sitebuilds"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A real Nuxt, SvelteKit or Astro app built by the preset, with the real tools: the packed output must pass the
// deploy path's archive checks and the worker must stay out of the public files. Needs Docker and the network.
func buildPresetForReal(t *testing.T, framework string, files map[string]string) *sites.Bundle {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	work := t.TempDir()
	for path, body := range files {
		write(t, filepath.Join(work, "src", path), body)
	}
	scripts := scriptDir(t)
	if out, err := exec.Command("chmod", "-R", "a+rwX", work, scripts).CombinedOutput(); err != nil {
		t.Fatalf("chmod: %v %s", err, out)
	}
	run := exec.Command("docker", "run", "--rm", "-u", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "-e", "HOME=/work/home", "-e", "CI=true",
		"-e", "npm_config_cache=/work/npm-cache", "-e", "FRAMEWORK="+framework, "-e", "ROOT_DIR=", "-e", "OUTPUT_DIR=", "-e", "HOLD_SECONDS=0",
		"-v", work+":/work", "-v", scripts+":/scripts:ro", "-w", "/work", sitebuilds.PinnedNodeImages["22"],
		"bash", "-c", "bash /scripts/build.sh; code=$?; echo BUILD_EXIT=$code; [ $code -eq 0 ] && bash /scripts/collect.sh; echo COLLECT_EXIT=$?")
	out, err := run.CombinedOutput()
	log := string(out)
	if err != nil || !strings.Contains(log, "BUILD_EXIT=0") || !strings.Contains(log, "COLLECT_EXIT=0") {
		t.Fatalf("docker run: %v\n%s", err, tail(log, 8000))
	}
	archive, err := os.Open(filepath.Join(work, "collect", "site.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	limits := sites.DefaultLimits
	limits.MaxServerBundleBytes = 30 << 20
	bundle, err := sites.ReadServerArchive(archive, limits, t.TempDir())
	if err != nil {
		t.Fatalf("the deploy path refused the packed output: %v\n%s", err, tail(log, 3000))
	}
	t.Cleanup(func() { _ = bundle.Close() })
	if bundle.Server.Framework != framework || len(bundle.Modules) == 0 || len(bundle.Files) == 0 {
		t.Errorf("server %+v modules %d assets %d", bundle.Server, len(bundle.Modules), len(bundle.Files))
	}
	for _, file := range bundle.Files {
		if strings.Contains(file.Path, "_worker.js") {
			t.Errorf("the worker is a public file: %s", file.Path)
		}
	}
	return bundle
}

func TestNuxtPreset_BuildsWithRealTools(t *testing.T) {
	buildPresetForReal(t, "nuxt", map[string]string{
		"package.json":   `{"name":"n","private":true,"type":"module","scripts":{"build":"nuxt build"},"dependencies":{"nuxt":"3.17.5","vue":"3.5.17","vue-router":"4.5.1"}}`,
		"app.vue":        `<template><h1>hello</h1></template>`,
		"nuxt.config.ts": `export default defineNuxtConfig({ compatibilityDate: "2025-07-15" })`,
	})
}

func TestAstroPreset_BuildsWithRealTools(t *testing.T) {
	buildPresetForReal(t, "astro", map[string]string{
		"package.json":          `{"name":"a","private":true,"type":"module","scripts":{"build":"astro build"},"dependencies":{"astro":"5.12.0","@astrojs/cloudflare":"12.6.0"}}`,
		"astro.config.mjs":      `import { defineConfig } from "astro/config"; import cloudflare from "@astrojs/cloudflare"; export default defineConfig({ output: "server", adapter: cloudflare() });`,
		"src/pages/index.astro": "---\n---\n<h1>hello</h1>",
	})
}

func TestSvelteKitPreset_BuildsWithRealTools(t *testing.T) {
	buildPresetForReal(t, "sveltekit", map[string]string{
		"package.json":            `{"name":"s","private":true,"type":"module","scripts":{"build":"vite build"},"devDependencies":{"@sveltejs/kit":"2.22.0","@sveltejs/adapter-cloudflare":"7.0.4","@sveltejs/vite-plugin-svelte":"6.0.0","svelte":"5.35.0","vite":"7.0.0"}}`,
		"svelte.config.js":        `import adapter from "@sveltejs/adapter-cloudflare"; export default { kit: { adapter: adapter() } };`,
		"vite.config.js":          `import { sveltekit } from "@sveltejs/kit/vite"; export default { plugins: [sveltekit()] };`,
		"src/app.html":            `<!doctype html><html><head>%sveltekit.head%</head><body>%sveltekit.body%</body></html>`,
		"src/routes/+page.svelte": `<h1>hello</h1>`,
		"src/routes/+layout.js":   `export const prerender = false;`,
	})
}
