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
