package shiptemplates

import (
	"strings"
	"testing"
)

func TestEveryStackAndVariantRenders(t *testing.T) {
	for _, stack := range Stacks() {
		for _, variant := range stack.Variants {
			t.Run(stack.Name+"/"+variant, func(t *testing.T) {
				file, err := Dockerfile(stack.Name, variant)
				if err != nil {
					t.Fatalf("Dockerfile: %v", err)
				}
				if !strings.HasPrefix(file.Content, "# syntax=docker/dockerfile:1\n") {
					t.Errorf("must start with the syntax line:\n%s", file.Content)
				}
				if !strings.Contains(file.Content, "EXPOSE 8080") || file.Port != 8080 {
					t.Errorf("every template serves on 8080:\n%s", file.Content)
				}
				if strings.Contains(file.Content, "{{") || strings.Contains(file.Content, "<no value>") {
					t.Errorf("unfilled placeholder:\n%s", file.Content)
				}
				if file.DockerIgnore == "" {
					t.Errorf("no .dockerignore")
				}
			})
		}
	}
}

func TestEveryTemplateRunsAsNonRootOnPortWithAHealthCheck(t *testing.T) {
	for _, stack := range Stacks() {
		for _, variant := range stack.Variants {
			file, err := Dockerfile(stack.Name, variant)
			if err != nil {
				t.Fatal(err)
			}
			name := stack.Name + "/" + variant
			nonRoot := strings.Contains(file.Content, "USER ") || strings.Contains(file.Content, ":nonroot") ||
				strings.Contains(file.Content, "nginx-unprivileged")
			if !nonRoot {
				t.Errorf("%s does not run as a non-root user", name)
			}
			if !strings.Contains(file.Content, "PORT=8080") {
				t.Errorf("%s does not set PORT=8080", name)
			}
			if !strings.HasPrefix(file.HealthCheckPath, "/") {
				t.Errorf("%s names no health check path", name)
			}
			if !strings.Contains(strings.Join(file.Notes, " "), file.HealthCheckPath) {
				t.Errorf("%s: the notes do not say how %s is served", name, file.HealthCheckPath)
			}
		}
	}
}

func TestNginxTemplatesServeTheirOwnHealthCheck(t *testing.T) {
	for _, stack := range []string{"static", "vite"} {
		file, err := Dockerfile(stack, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(file.Content, "location = /healthz") || file.HealthCheckPath != "/healthz" {
			t.Errorf("%s:\n%s", stack, file.Content)
		}
	}
	file, _ := Dockerfile("static", "")
	if strings.Contains(file.Content, "npm") || !strings.Contains(file.Content, "COPY . /usr/share/nginx/html") {
		t.Errorf("a static site is copied as is, with no build:\n%s", file.Content)
	}
	if !strings.Contains(file.Content, `location ~ /\. {`) || !strings.Contains(file.DockerIgnore, "Dockerfile") ||
		strings.Contains(file.DockerIgnore, "dist") {
		t.Errorf("a static site serves no dotfiles or build files, and may be a dist/ folder: %s\n%s", file.Content, file.DockerIgnore)
	}
}

func TestAnUnnamedStackAnswersQuestionsNotAGuess(t *testing.T) {
	questions := Questions()
	if len(questions.Ask) == 0 || len(questions.Detect) == 0 {
		t.Fatalf("questions = %+v", questions)
	}
	for _, stack := range Stacks() {
		if _, ok := questions.Detect[stack.Name]; !ok {
			t.Errorf("no hint tells %s apart", stack.Name)
		}
	}
}

func TestDockerfileDefaultsAndRefusals(t *testing.T) {
	file, err := Dockerfile("nextjs", "")
	if err != nil || !strings.Contains(file.Content, "npm ci") || !strings.Contains(file.Content, ".next/standalone") {
		t.Fatalf("nextjs default: %v\n%s", err, file.Content)
	}
	file, err = Dockerfile("node", "pnpm")
	if err != nil || !strings.Contains(file.Content, "pnpm install --frozen-lockfile") || !strings.Contains(file.Content, "pnpm-lock.yaml") {
		t.Fatalf("node/pnpm: %v\n%s", err, file.Content)
	}
	file, err = Dockerfile("java", "quarkus")
	if err != nil || !strings.Contains(file.Content, "quarkus-run.jar") {
		t.Fatalf("java/quarkus: %v\n%s", err, file.Content)
	}
	if _, err := Dockerfile("cobol", ""); err == nil {
		t.Errorf("an unknown stack must be refused")
	}
	if _, err := Dockerfile("go", "yarn"); err == nil {
		t.Errorf("an unknown variant must be refused")
	}
}

// A server image runs node itself under an init, never a package manager:
// npm as PID 1 forwards SIGTERM poorly, and the build's dev tools (nest,
// tsc) are pruned from the image (EXC-600).
func TestNodeServersRunNodeUnderAnInit(t *testing.T) {
	for _, stack := range []string{"node", "nestjs"} {
		for _, variant := range nodeVariants {
			file, err := Dockerfile(stack, variant)
			if err != nil {
				t.Fatalf("%s/%s: %v", stack, variant, err)
			}
			for _, want := range []string{`ENTRYPOINT ["/sbin/tini", "--"]`, `CMD ["node", `, "apk add --no-cache tini"} {
				if !strings.Contains(file.Content, want) {
					t.Errorf("%s/%s lacks %q:\n%s", stack, variant, want, file.Content)
				}
			}
			for _, refused := range []string{`"npm", "start"`, `"pnpm", "start"`, `"start"]`} {
				if strings.Contains(file.Content, refused) {
					t.Errorf("%s/%s still starts through the package manager:\n%s", stack, variant, file.Content)
				}
			}
		}
	}
}

func TestNestJSRunsItsBuiltMain(t *testing.T) {
	file, err := Dockerfile("nestjs", "")
	if err != nil || !strings.Contains(file.Content, `CMD ["node", "dist/main.js"]`) || !strings.Contains(file.Content, "npm run build") {
		t.Fatalf("nestjs: %v\n%s", err, file.Content)
	}
	if detect := Questions().Detect["nestjs"]; !strings.Contains(detect, "@nestjs/core") {
		t.Fatalf("the questions do not tell NestJS apart: %q", detect)
	}
}
