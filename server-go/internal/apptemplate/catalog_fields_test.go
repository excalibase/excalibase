package apptemplate

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseReadsCategoryLicenseAndMinMemory(t *testing.T) {
	source := replace("description: Longer text.\n", "description: Longer text.\ncategory: cache\nlicense: BSD-3-Clause\n")
	source = strings.Replace(source, "    internalPorts: [6379]\n", "    internalPorts: [6379]\n    minMemory: 1Gi\n", 1)
	tpl := mustParse(t, source)
	if tpl.Category != "cache" || tpl.License != "BSD-3-Clause" || tpl.Apps[0].MinMemory != "1Gi" {
		t.Fatalf("parsed %+v / %+v", tpl, tpl.Apps[0])
	}
	if got, err := tpl.Apps[0].MinMemoryBytes(); err != nil || got != 1<<30 {
		t.Fatalf("min memory bytes: %d, %v", got, err)
	}
	if got, err := tpl.Apps[1].MinMemoryBytes(); err != nil || got != 0 {
		t.Fatalf("no min memory is 0, got %d, %v", got, err)
	}
}

func TestParseRefusesABadCategoryLicenseOrMinMemory(t *testing.T) {
	cases := map[string][2]string{
		"an unknown category":   {"description: Longer text.\n", "description: Longer text.\ncategory: crypto\n"},
		"a licence too long":    {"description: Longer text.\n", "description: Longer text.\nlicense: " + strings.Repeat("x", 81) + "\n"},
		"a min memory unparsed": {"    internalPorts: [6379]\n", "    internalPorts: [6379]\n    minMemory: lots\n"},
		"a min memory too big":  {"    internalPorts: [6379]\n", "    internalPorts: [6379]\n    minMemory: 64Gi\n"},
		"a min memory negative": {"    internalPorts: [6379]\n", "    internalPorts: [6379]\n    minMemory: -1Gi\n"},
	}
	wants := map[string]string{
		"an unknown category":   "category",
		"a licence too long":    "license",
		"a min memory unparsed": "minMemory",
		"a min memory too big":  "minMemory",
		"a min memory negative": "minMemory",
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { refusal(t, replace(c[0], c[1]), wants[name]) })
	}
}

func TestShowOnceIsOnlyForGeneratedValues(t *testing.T) {
	source := replace("        value: \"${{ apps.redis.env.REDIS_PASSWORD }}\"\n", "        value: \"${{ apps.redis.env.REDIS_PASSWORD }}\"\n        showOnce: true\n")
	tpl := mustParse(t, source)
	if !tpl.Apps[1].Env[2].ShowOnce {
		t.Fatalf("showOnce not read: %+v", tpl.Apps[1].Env[2])
	}
	refusal(t, replace("        value: hello\n", "        value: hello\n        showOnce: true\n"), "showOnce")
	refusal(t, replace("        value: \"${{ apps.redis.host }}\"\n", "        value: \"${{ apps.redis.host }}\"\n        showOnce: true\n"), "showOnce")
}

func TestBuildReturnsShownValuesOnlyAndNeverPrintsThem(t *testing.T) {
	source := replace("        value: \"redis://:${{ apps.redis.env.REDIS_PASSWORD }}@${{ apps.redis.host }}:${{ apps.redis.port }}/0\"\n",
		"        value: \"redis://:${{ apps.redis.env.REDIS_PASSWORD }}@${{ apps.redis.host }}:${{ apps.redis.port }}/0\"\n        showOnce: true\n")
	tpl := mustParse(t, source)
	ids := 0
	built, err := tpl.Build(BuildInput{
		ProjectID: "p1", Tier: "FREE",
		NewID:  func() string { ids++; return strings.Repeat(string(rune('a'+ids)), 8) },
		Secret: func(n int) (string, error) { return strings.Repeat("S", n), nil },
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(built.Shown) != 1 || built.Shown[0].App != "web" || built.Shown[0].Name != "REDIS_URL" ||
		built.Shown[0].Value != "redis://:"+strings.Repeat("S", 32)+"@redis:6379/0" {
		t.Fatalf("shown: %d values", len(built.Shown))
	}
	for _, printed := range []string{built.Shown.String(), fmt.Sprintf("%v %+v %#v", built.Shown, built.Shown, built.Shown)} {
		if strings.Contains(printed, "SSSS") {
			t.Fatal("a shown value was printed")
		}
	}
}

// A showOnce value two references away never reaches past the first app.
func TestShowOnceReadsOnlyOneStep(t *testing.T) {
	source := replace("      - name: GREETING\n        value: hello\n", "      - name: GREETING\n        value: hello\n      - name: TOKEN\n        value: \"${{ apps.redis.env.REDIS_PASSWORD }}x\"\n        showOnce: true\n")
	if _, err := Parse([]byte(source)); err != nil {
		t.Fatalf("parse: %v", err)
	}
}
