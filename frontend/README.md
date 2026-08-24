# Excalibase Studio

React + TypeScript + Vite frontend for the Excalibase database provisioning platform. Talks to the Go server (`server-go/`) at `http://localhost:24005`.

## Features

- **Setup wizard** — vault initialization (Shamir shares + threshold) and first-admin registration in a single guided flow at `/setup`
- **Dashboard** — list of all instances, status pills, deletion-protection toggle
- **Provisioning flow visualizer** — real-time 9-stage pipeline with rollback log on failure
- **Project creation** — three modes: Kubernetes (CNPG), Docker (containers), or BYOC (register an external DB)
- **Schema browser** — tables, columns, roles, extensions, RLS policies, functions, triggers, indexes; row CRUD with paginated, sortable, filterable views
- **Edge functions** — multi-file Deno functions with Monaco editor, secrets management, log streaming, public invoke URL
- **Realtime** — per-table CDC publication toggle (enables/disables logical replication membership)
- **Vault secrets browser** — read/write secrets with PAT-scoped paths
- **Performance + advisors** — pg_stat_statements top queries, performance/security advisors
- **Backups + PITR** — manual backup trigger, list, restore to point in time
- **Org + project members** — invite by email, role management, project-level membership separate from org membership
- **Auth + tokens** — login, register (first registrant becomes platform_admin in self-hosted mode), PAT lifecycle

## Tech Stack

- React 18 (do **not** upgrade to 19) + TypeScript
- Vite for build tooling
- TanStack Query for server state, TanStack Table for data grids
- Tailwind CSS, lucide-react, cmdk
- CodeMirror 6 + @uiw/react-codemirror for SQL editor
- Monaco Editor for the edge functions editor
- axios pinned to `1.13.5` (do **not** upgrade — `1.14.x` was a supply-chain compromise)
- Vitest + React Testing Library for unit tests
- Playwright for end-to-end tests

## Setup

```bash
# Install
npm install

# Start dev server (defaults to http://localhost:5173)
npm run dev

# Build for production
npm run build

# Preview production build
npm run preview
```

The Vite dev server proxies API calls to `http://localhost:24005`. Override via `VITE_API_URL` if the backend lives elsewhere.

### Environment variables

| Variable                          | Purpose                                                                 | Default                                |
|-----------------------------------|-------------------------------------------------------------------------|----------------------------------------|
| `VITE_API_URL`                    | Go provisioning API base URL                                            | `http://localhost:24005/api`           |
| `VITE_EXCALIBASE_GRAPHQL_WS_URL`  | excalibase-graphql realtime WebSocket — drives live data on TablesPage  | `ws://localhost:10000/api/v1/realtime` |

The studio's TablesPage subscribes to `VITE_EXCALIBASE_GRAPHQL_WS_URL` when a table is open so the data grid stays in sync without polling. The hook sends `connection_init` with the user's bearer JWT, subscribes to the selected collection, and re-runs the rows query on every CDC event. Auto-reconnect uses exponential backoff (1s → 30s with ±20% jitter).

## Backend

The Go backend must be running for the app to work. From the repo root:

```bash
cd server-go
go build -o excalibase-server ./cmd/server/
PORT=24005 STORAGE_PATH=../provisioning-data CORS_ORIGINS=http://localhost:5173 ./excalibase-server
```

On first run, the studio will redirect to `/setup` to initialize the vault and create the first platform_admin.

## Project Structure

```
src/
├── api/             axios instance + typed request helpers
├── components/      shared layout (sidebar, command palette, modals, tables)
├── pages/           route components (Dashboard, Project, Schema, Functions, Realtime, ...)
├── hooks/           TanStack Query hooks (useInstances, useProvision, useFunctions, ...)
├── types/           TS types mirroring Go domain entities
├── utils/           cn, formatters, vault path helpers
├── App.tsx          router + auth guard
└── main.tsx         entry point
```

## Testing

```bash
# Vitest unit + component tests
npm run test
npm run test:ui          # interactive UI

# Playwright E2E (26 spec files, ~120 tests; requires backend at localhost:24005)
npx playwright test
npx playwright test --ui
npx playwright test e2e/byoc.spec.ts        # one file
```

E2E spec coverage: vault setup, BYOC flow, Docker-mode flow, K8s-mode flow, edge functions, realtime, schema CRUD, advisors, RBAC (auth/users), org members.

## Conventions

- **Server state via TanStack Query** — never `useEffect` for data fetching
- **No barrel exports** — import from the file that owns the symbol
- **Two navbars** — separate platform sidebar and project-scoped sidebar; no "back to admin" tabs (handled at routing level)
- **Dark mode** is the default and only theme

## Design System

```
Background: #1a1d2e (primary), #252938 (secondary)
Text:       #e4e6eb (primary), #9ca3af (secondary)
Accent:     #3b82f6 (blue)
Success:    #10b981 (green)
Warning:    #f59e0b (orange)
Error:      #ef4444 (red)
```
