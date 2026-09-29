# Private network between a project's apps (EXC-524)

Off by default: a Containers app accepts traffic only from the edge (its public URL) and the node's health probes (EXC-500).

An org admin or owner can turn it on per project (Studio: project Settings, "Private network between apps"; API: `PUT /api/projects/{projectId}/app-network` with `{"privateNetwork": true}`). While it is on:

- every app in the project reaches every other app in the same project by its name: `http://<app name>` (Service port 80, forwarded to the app's HTTP port);
- nothing else gains access: apps in other projects (even with their own setting on), the project's database, other pods in the namespace, the platform and the internet are still refused.

## Apps per project

How many apps one project may hold is set per plan (owner decision 2026-09-29): FREE 2, STANDARD 5, ENTERPRISE 20. They are defaults in the tier configuration that a platform admin can edit (`maxApps`, 0 offers none). A create above the limit is refused with 409 naming it. A downgrade or a restore never deletes apps: the project keeps them and new ones are refused until it is under the limit. Every app still passes the plan's CPU/memory admission at deploy (EXC-388). The project namespace's object quota is sized from the plan when the project is created, restored or gains a database, whenever a plan is edited or an org's plan changes, and at each deploy (pods: 20 + apps × (max replicas + 2); PVCs: at least 7, database instances + 2 per app for its disk and the copy made while lowering it; Services: 15 + apps, where apps is the plan's count or the apps held if more), so the quota never refuses what the plan allows, and never drops below what the namespace already holds (a downgrade keeps what runs and refuses what is new); storage bytes stay governed by the plan caps and the storage budget.

## How it is enforced

One CiliumNetworkPolicy, `apps-private-network`, exists in the project namespace only while the setting is on. It selects app pods (`excalibase.io/component=app`), admits ingress only from app pods of the same namespace on the named container port `http`, and allows egress only to app pods of the same namespace. Turning it off deletes the policy, so the per-app policies are exactly what they are without the feature.

The cluster is changed first and the setting recorded after, so the setting never says "on" for a policy that is not there, and a failed write closes the network again.

## Names

Each app's Service is named after the app. App names are DNS labels (start with a letter, end with a letter or digit, 2-50 characters) and may not start with `proj-` or be `deno-runtime`: those are the Service names the platform keeps in the namespace (database `-rw`/`-ro`/`-r`, DocumentDB gateway, public endpoints, function runtime). Provisioning also refuses to update a Service of that name that the app did not render.

## Internal TCP ports (EXC-525)

An app may declare up to 8 internal ports (`"internalPorts": [{"port": 6379, "protocol": "TCP"}]`; Studio: "Internal TCP ports"). Each is TCP only, between 1024 and 65535, never the app's HTTP port, and listed once. Each becomes a named container port (`internal-1` ... `internal-8`) and a Service port under its own number, so the project's apps reach it at `<app name>:<port>`.

Internal ports are reachable only from the same project's apps while the private network is on: the edge fence and the Ingress name only the HTTP port, and the private network policy admits the port names only from the project's app pods.

An app can instead be an **internal service** (`"internal": true`, Studio: "Internal service"): no HTTP port, no health path, at least one internal port. It gets no Ingress and no URL, its own fence admits only the node's readiness probe on its first internal port, and a custom domain is refused for it. Deploying a public app as an internal service removes the route it had. It is reachable only by the project's apps, with the private network on, at `<app name>:<port>` (e.g. `cache:6379`).
