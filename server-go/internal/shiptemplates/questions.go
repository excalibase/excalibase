package shiptemplates

// StackQuestions is the answer when no stack is named: what to look at in the
// repository, and what to ask the user when that does not settle it.
type StackQuestions struct {
	Detect map[string]string `json:"detect"`
	Ask    []string          `json:"ask"`
	Stacks []Stack           `json:"stacks"`
}

// Questions tells a stack apart from the repository's files rather than guess one.
func Questions() StackQuestions {
	return StackQuestions{
		Detect: map[string]string{
			"nextjs": "package.json depends on next (next.config.*)",
			"vite":   "package.json depends on vite and builds a browser app (index.html at the root, dist/ output)",
			"node":   "package.json with a start script that runs a server (express, fastify, hono, ...)",
			"static": "plain .html, .css and .js files and no package.json, or a ready folder such as public/",
			"python": "requirements.txt; gunicorn for Flask or Django, uvicorn for FastAPI or Starlette",
			"go":     "go.mod with a main package",
			"java":   "pom.xml (spring-boot or quarkus, by its dependencies) or build.gradle (spring-boot-gradle)",
		},
		Ask: []string{
			"Which folder holds the app, if the repository has more than one?",
			"Is it a browser-only page (vite or static) or a server that must run (node, nextjs, python, go, java)?",
			"Which package manager does a Node app use: npm (package-lock.json) or pnpm (pnpm-lock.yaml)?",
		},
		Stacks: Stacks(),
	}
}
