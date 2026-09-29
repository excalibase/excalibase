package apptemplate

import (
	"errors"
	"strings"
	"testing"
)

const webRedisSource = `
format: excalibase.template/v1
id: web-redis
name: Web app + Redis
summary: A public web app with a private Redis.
description: Longer text.
apps:
  - name: redis
    image: redis:7.4-alpine
    internal: true
    internalPorts: [6379]
    replicas: 1
    args: ["--requirepass", "$(REDIS_PASSWORD)"]
    disk: {mountPath: /data, size: 1Gi}
    env:
      - name: REDIS_PASSWORD
        value: "${{ secret(32) }}"
  - name: web
    image: nginxinc/nginx-unprivileged:1.27
    port: 8080
    replicas: 1
    env:
      - name: REDIS_HOST
        value: "${{ apps.redis.host }}"
      - name: REDIS_PORT
        value: "${{apps.redis.port}}"
      - name: REDIS_PASSWORD
        value: "${{ apps.redis.env.REDIS_PASSWORD }}"
      - name: REDIS_URL
        value: "redis://:${{ apps.redis.env.REDIS_PASSWORD }}@${{ apps.redis.host }}:${{ apps.redis.port }}/0"
      - name: GREETING
        value: hello
`

func mustParse(t *testing.T, source string) *Template {
	t.Helper()
	tpl, err := Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tpl
}

func TestParseAcceptsAValidTemplate(t *testing.T) {
	tpl := mustParse(t, webRedisSource)
	if tpl.ID != "web-redis" || len(tpl.Apps) != 2 || tpl.Apps[0].Disk.Size != "1Gi" {
		t.Fatalf("parsed %+v", tpl)
	}
}

func refusal(t *testing.T, source, want string) {
	t.Helper()
	_, err := Parse([]byte(source))
	if err == nil || !errors.Is(err, ErrInvalidTemplate) || !strings.Contains(err.Error(), want) {
		t.Fatalf("want an invalid-template error containing %q, got %v", want, err)
	}
}

func replace(old, new string) string { return strings.Replace(webRedisSource, old, new, 1) }

func TestParseRefusesWhatTheFormatDoesNotAllow(t *testing.T) {
	cases := map[string][2]string{
		"an unknown field":                        {"    replicas: 1\n    args", "    replicas: 1\n    hostPath: /etc\n    args"},
		"another format":                          {"excalibase.template/v1", "excalibase.template/v2"},
		"a bad id":                                {"id: web-redis", "id: Web_Redis"},
		"no name":                                 {"name: Web app + Redis", "name: \"\""},
		"an image with no tag":                    {"image: redis:7.4-alpine", "image: redis"},
		"two apps with one name":                  {"  - name: web\n", "  - name: redis\n"},
		"a reserved app name":                     {"  - name: web\n", "  - name: proj-web\n"},
		"missing replicas":                        {"    port: 8080\n    replicas: 1\n", "    port: 8080\n"},
		"a disk above what anyone may have":       {"size: 1Gi", "size: 100000Gi"},
		"a disk over a system path":               {"mountPath: /data", "mountPath: /etc"},
		"an internal port clash":                  {"internalPorts: [6379]", "internalPorts: [6379, 6379]"},
		"an unknown function":                     {"secret(32)", "random(32)"},
		"a short secret":                          {"secret(32)", "secret(8)"},
		"a huge secret":                           {"secret(32)", "secret(4096)"},
		"an unterminated expression":              {"${{ apps.redis.host }}\"", "${{ apps.redis.host \""},
		"an unknown app":                          {"${{ apps.redis.host }}\"", "${{ apps.cache.host }}\""},
		"an unknown app variable":                 {"${{ apps.redis.env.REDIS_PASSWORD }}\"\n", "${{ apps.redis.env.NOPE }}\"\n"},
		"an unknown app field":                    {"${{ apps.redis.host }}\"", "${{ apps.redis.ip }}\""},
		"a database variable that does not exist": {"value: hello", "value: \"${{ db.PASSWORD }}\""},
		"a database reference inside text":        {"value: hello", "value: \"url=${{ db.DATABASE_URL }}\""},
		"a duplicate variable":                    {"      - name: GREETING", "      - name: REDIS_HOST"},
		"a bad variable name":                     {"      - name: GREETING", "      - name: 9GREETING"},
		"an empty arg":                            {"args: [\"--requirepass\", \"$(REDIS_PASSWORD)\"]", "args: [\"\"]"},
		"an internal app with an HTTP port":       {"    internal: true\n", "    internal: true\n    port: 80\n"},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			source := replace(edit[0], edit[1])
			if source == webRedisSource {
				t.Fatalf("the edit %q did not apply", edit[0])
			}
			_, err := Parse([]byte(source))
			if err == nil || !errors.Is(err, ErrInvalidTemplate) {
				t.Fatalf("want an invalid-template error, got %v", err)
			}
		})
	}
}

func TestParseRefusesAChainOfAppVariables(t *testing.T) {
	source := replace("      - name: GREETING\n        value: hello",
		"      - name: GREETING\n        value: \"${{ apps.web.env.REDIS_PASSWORD }}\"")
	refusal(t, source, "points at another app variable")
}

func TestParseRefusesAnOversizedTemplate(t *testing.T) {
	source := webRedisSource + "# " + strings.Repeat("x", MaxTemplateBytes) + "\n"
	refusal(t, source, "exceeds")
}

func TestParseRefusesMoreAppsThanAnyPlanHolds(t *testing.T) {
	var b strings.Builder
	b.WriteString("format: excalibase.template/v1\nid: many\nname: Many\nsummary: s\napps:\n")
	for i := 0; i <= MaxTemplateApps; i++ {
		b.WriteString("  - name: app" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\n    image: nginx:1.27\n    port: 8080\n    replicas: 1\n")
	}
	refusal(t, b.String(), "at most")
}

func TestFacts(t *testing.T) {
	tpl := mustParse(t, webRedisSource)
	facts := tpl.Facts()
	if !facts.NeedsPrivateNetwork || facts.NeedsDatabase || !facts.GeneratesSecrets || facts.Apps != 2 {
		t.Fatalf("facts %+v", facts)
	}
	if facts.DiskBytes != 1<<30 {
		t.Fatalf("disk bytes %d", facts.DiskBytes)
	}
}

// Two public apps that only share a generated value never talk to each other.
func TestFactsSharingAValueNeedsNoNetwork(t *testing.T) {
	tpl := mustParse(t, `
format: excalibase.template/v1
id: pair
name: Pair
summary: s
apps:
  - name: one
    image: nginx:1.27
    port: 8080
    replicas: 1
    env:
      - {name: TOKEN, value: "${{ secret(32) }}"}
      - {name: DATABASE_URL, value: "${{ db.DATABASE_URL }}"}
  - name: two
    image: nginx:1.27
    port: 8080
    replicas: 1
    env:
      - {name: TOKEN, value: "${{ apps.one.env.TOKEN }}"}
      - {name: SELF, value: "${{ apps.two.host }}"}
`)
	facts := tpl.Facts()
	if facts.NeedsPrivateNetwork || !facts.NeedsDatabase {
		t.Fatalf("facts %+v", facts)
	}
}

func TestValueSource(t *testing.T) {
	for value, want := range map[string]string{
		"hello":                                     SourceLiteral,
		"${{ secret(32) }}":                         SourceGenerated,
		"${{ db.DATABASE_URL }}":                    SourceDatabase,
		"${{ apps.redis.host }}:6379":               SourceApp,
		"redis://:${{ apps.redis.env.PASSWORD }}@x": SourceApp,
	} {
		if got := ValueSource(value); got != want {
			t.Errorf("%q: got %s, want %s", value, got, want)
		}
	}
}
