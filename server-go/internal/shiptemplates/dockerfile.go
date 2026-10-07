// Package shiptemplates holds the Dockerfiles and CI pipelines a project
// ships with (ADR 0037, phase 1): the user's CI builds and pushes to their own
// registry, then calls the deploy API. Every image serves on port 8080.
package shiptemplates

import (
	"fmt"
	"slices"
	"strings"
)

// Port is the port every template serves on; set the app's port to it.
const Port = 8080

// Stack is one supported stack and its variants; the first is the default.
type Stack struct {
	Name     string   `json:"name"`
	Variants []string `json:"variants"`
}

// File is a rendered Dockerfile with what to know before using it.
type File struct {
	Path         string `json:"path"`
	Content      string `json:"content"`
	DockerIgnore string `json:"dockerignore"`
	Port         int    `json:"port"`
	// HealthCheckPath is what to pass as the app's health check path.
	HealthCheckPath string   `json:"healthCheckPath"`
	Notes           []string `json:"notes"`
}

// nodeTool is the install and run commands of one Node package manager.
type nodeTool struct {
	setup, lock, install, run, prune string
}

var nodeTools = map[string]nodeTool{
	"npm": {lock: "package-lock.json", install: "npm ci", run: "npm run", prune: "npm prune --omit=dev"},
	"pnpm": {setup: "RUN corepack enable\n", lock: "pnpm-lock.yaml", install: "pnpm install --frozen-lockfile",
		run: "pnpm run", prune: "pnpm prune --prod"},
}

var nodeVariants = []string{"npm", "pnpm"}

// Stacks lists what Dockerfile can render.
func Stacks() []Stack {
	return []Stack{
		{Name: "node", Variants: nodeVariants},
		{Name: "nextjs", Variants: nodeVariants},
		{Name: "vite", Variants: nodeVariants},
		{Name: "static", Variants: []string{"default"}},
		{Name: "python", Variants: []string{"gunicorn", "uvicorn"}},
		{Name: "go", Variants: []string{"default"}},
		{Name: "java", Variants: []string{"spring-boot", "spring-boot-gradle", "quarkus"}},
	}
}

// Dockerfile renders the Dockerfile for a stack; an empty variant is the
// stack's default.
func Dockerfile(stack, variant string) (File, error) {
	index := slices.IndexFunc(Stacks(), func(s Stack) bool { return s.Name == stack })
	if index < 0 {
		return File{}, fmt.Errorf("unknown stack %q: use node, nextjs, vite, static, python, go or java", stack)
	}
	known := Stacks()[index].Variants
	if variant == "" {
		variant = known[0]
	}
	if !slices.Contains(known, variant) {
		return File{}, fmt.Errorf("unknown variant %q for %s: use %s", variant, stack, strings.Join(known, ", "))
	}
	content, notes := render(stack, variant)
	health, healthNote := healthCheck(stack, variant)
	ignore := dockerIgnore
	if stack == "static" {
		ignore = staticDockerIgnore
	}
	return File{
		Path: "Dockerfile", Content: content, DockerIgnore: ignore, Port: Port, HealthCheckPath: health,
		Notes: append(notes, healthNote, "Set the app's port to 8080 in Excalibase. The image runs as a non-root user."),
	}, nil
}

// healthCheck is the path a stack answers health checks on, and how.
func healthCheck(stack, variant string) (string, string) {
	pass := " Pass it as health_check_path to create_app."
	switch {
	case stack == "vite" || stack == "static":
		return "/healthz", "nginx answers GET /healthz with 200 itself." + pass
	case variant == "spring-boot" || variant == "spring-boot-gradle":
		return "/actuator/health", "/actuator/health needs the spring-boot-starter-actuator dependency." + pass
	case variant == "quarkus":
		return "/q/health", "/q/health needs the quarkus-smallrye-health extension." + pass
	}
	return "/healthz", "Add a GET /healthz route that answers 200 without touching the database." + pass
}

func render(stack, variant string) (string, []string) {
	switch stack {
	case "node", "nextjs", "vite":
		return renderNode(stack, nodeTools[variant])
	case "python":
		return renderPython(variant)
	case "go":
		return goDockerfile, []string{"Builds the main package at the repository root; change the go build path for a cmd/ layout."}
	case "static":
		return staticDockerfile, []string{"Serves the repository's files as they are, with no build step: change COPY . to the site's folder (e.g. COPY public/) if it has one."}
	}
	return renderJava(variant)
}

func renderNode(stack string, tool nodeTool) (string, []string) {
	templates := map[string]string{"node": nodeDockerfile, "nextjs": nextDockerfile, "vite": viteDockerfile}
	notes := map[string][]string{
		"node":   {"Runs the start script of package.json; the server must listen on process.env.PORT."},
		"nextjs": {`Needs output: "standalone" in next.config, and a public/ directory (an empty one is fine).`},
		"vite":   {"Serves the built dist/ with nginx and falls back to index.html for client-side routes."},
	}
	content := strings.NewReplacer(
		"{{setup}}", tool.setup, "{{lock}}", tool.lock, "{{install}}", tool.install,
		"{{run}}", tool.run, "{{prune}}", tool.prune,
	).Replace(templates[stack])
	return content, notes[stack]
}

func renderPython(variant string) (string, []string) {
	command := `exec gunicorn --bind 0.0.0.0:$PORT app:app`
	note := "Runs app:app with gunicorn (Flask, Django's wsgi module); add gunicorn to requirements.txt and change the module if needed."
	if variant == "uvicorn" {
		command = `exec uvicorn main:app --host 0.0.0.0 --port $PORT`
		note = "Runs main:app with uvicorn (FastAPI, Starlette); add uvicorn to requirements.txt and change the module if needed."
	}
	return strings.Replace(pythonDockerfile, "{{command}}", command, 1), []string{note}
}

func renderJava(variant string) (string, []string) {
	switch variant {
	case "spring-boot-gradle":
		return springGradleDockerfile, []string{"Uses the Gradle wrapper (gradlew) committed to the repository."}
	case "quarkus":
		return quarkusDockerfile, []string{"Uses the Maven wrapper and the default fast-jar package type."}
	}
	return springMavenDockerfile, []string{"Uses the Maven wrapper (mvnw) committed to the repository."}
}

const dockerIgnore = `.git
.env
.env.*
node_modules
dist
build
.next
target
__pycache__
*.log
`

const nodeDockerfile = `# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
{{setup}}COPY package.json {{lock}} ./
RUN {{install}}
COPY . .
RUN {{run}} --if-present build
RUN {{prune}}

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production PORT=8080
COPY --from=build --chown=node:node /app ./
USER node
EXPOSE 8080
CMD ["npm", "start"]
`

const nextDockerfile = `# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
{{setup}}COPY package.json {{lock}} ./
RUN {{install}}
COPY . .
RUN {{run}} build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production PORT=8080 HOSTNAME=0.0.0.0
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
COPY --from=build --chown=node:node /app/public ./public
USER node
EXPOSE 8080
CMD ["node", "server.js"]
`

const viteDockerfile = `# syntax=docker/dockerfile:1
FROM node:22-alpine AS build
WORKDIR /app
{{setup}}COPY package.json {{lock}} ./
RUN {{install}}
COPY . .
RUN {{run}} build

FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY --from=build /app/dist /usr/share/nginx/html
COPY <<'EOF' /etc/nginx/conf.d/default.conf
server {
    listen 8080;
    root /usr/share/nginx/html;
    location = /healthz {
        access_log off;
        default_type text/plain;
        return 200 "ok\n";
    }
    location / {
        try_files $uri $uri/ /index.html;
    }
}
EOF
ENV PORT=8080
EXPOSE 8080
`

const staticDockerfile = `# syntax=docker/dockerfile:1
FROM nginxinc/nginx-unprivileged:1.27-alpine
COPY . /usr/share/nginx/html
COPY <<'EOF' /etc/nginx/conf.d/default.conf
server {
    listen 8080;
    root /usr/share/nginx/html;
    location = /healthz {
        access_log off;
        default_type text/plain;
        return 200 "ok\n";
    }
    location ~ /\. {
        deny all;
    }
    location / {
        try_files $uri $uri/ =404;
    }
}
EOF
ENV PORT=8080
EXPOSE 8080
`

// staticDockerIgnore keeps what builds and ships the site out of a site
// served as it is.
const staticDockerIgnore = `.git
.env
.env.*
.*
Dockerfile
Jenkinsfile
node_modules
*.log
*.md
`

const pythonDockerfile = `# syntax=docker/dockerfile:1
FROM python:3.12-slim
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PORT=8080
WORKDIR /app
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
RUN useradd --create-home app
USER app
EXPOSE 8080
CMD ["sh", "-c", "{{command}}"]
`

const goDockerfile = `# syntax=docker/dockerfile:1
FROM golang:1-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app"]
`

const springMavenDockerfile = `# syntax=docker/dockerfile:1
FROM eclipse-temurin:21-jdk AS build
WORKDIR /src
COPY . .
RUN ./mvnw -B -DskipTests package && cp target/*.jar /app.jar

FROM eclipse-temurin:21-jre
COPY --from=build /app.jar /app/app.jar
ENV PORT=8080 SERVER_PORT=8080
USER 1000
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
`

const springGradleDockerfile = `# syntax=docker/dockerfile:1
FROM eclipse-temurin:21-jdk AS build
WORKDIR /src
COPY . .
RUN ./gradlew --no-daemon bootJar && cp build/libs/*.jar /app.jar

FROM eclipse-temurin:21-jre
COPY --from=build /app.jar /app/app.jar
ENV PORT=8080 SERVER_PORT=8080
USER 1000
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/app/app.jar"]
`

const quarkusDockerfile = `# syntax=docker/dockerfile:1
FROM eclipse-temurin:21-jdk AS build
WORKDIR /src
COPY . .
RUN ./mvnw -B -DskipTests package

FROM eclipse-temurin:21-jre
WORKDIR /app
COPY --from=build /src/target/quarkus-app/lib/ ./lib/
COPY --from=build /src/target/quarkus-app/*.jar ./
COPY --from=build /src/target/quarkus-app/app/ ./app/
COPY --from=build /src/target/quarkus-app/quarkus/ ./quarkus/
ENV PORT=8080 QUARKUS_HTTP_PORT=8080 QUARKUS_HTTP_HOST=0.0.0.0
USER 1000
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "/app/quarkus-run.jar"]
`
