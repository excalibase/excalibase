# Templates: a set of apps, disks and variables in one deploy (EXC-526)

A template declares one or more Containers apps: their images, ports, disks, start arguments and variables. Deploying it into a project creates and deploys all of them, or none of them.

Studio: a project's **Containers → Templates**. API: `GET /api/projects/{projectId}/app-templates/` (any member) lists every template with what it costs this project; `GET .../app-templates/{templateId}` adds the template's source; `POST .../app-templates/{templateId}/deploy` (Developer and up) deploys it.

Only the built-in templates exist for now. They ship inside the server binary (read-only), and a built-in that does not parse stops the server at start. Customer-uploaded templates are not offered.

## Built-in templates

| id | What it creates | Needs |
|----|-----------------|-------|
| `redis` | `redis`: Redis 7.4 as an internal service on `redis:6379`, 1Gi disk at `/data` (append-only file), password generated on deploy | private network |
| `web-redis` | `redis` as above, plus `web`: a public web app (placeholder image nginx-unprivileged 1.27 on 8080) with `REDIS_HOST`, `REDIS_PORT`, `REDIS_PASSWORD`, `REDIS_URL` | private network |
| `web-postgres` | `web`: a public web app with `DATABASE_URL` pointing at the project's database | the project's database, ready |

Images are pinned by digest. The web image is a placeholder: change the app's image to your own after the deploy.

## Format (`excalibase.template/v1`)

```yaml
format: excalibase.template/v1   # the only version read
id: web-redis                    # 2-50 lowercase letters, digits, hyphens
name: Web app + Redis starter    # 1-80 characters
summary: One line.               # 1-200 characters
description: Longer text.        # optional, up to 4000 characters
apps:                            # 1-20 apps
  - name: redis                  # the app name, also its host name in the project
    image: redis@sha256:...      # an explicit tag or digest; a bare name is refused
    internal: true               # an internal service: no HTTP port, no URL
    internalPorts: [6379]        # TCP, 1024-65535, at most 8
    replicas: 1                  # required, 0-3 (and within the plan)
    args: ["--requirepass", "$(REDIS_PASSWORD)"]   # replace the image's CMD
    disk: {mountPath: /data, size: 1Gi}             # one disk, within the plan's cap
    env:
      - name: REDIS_PASSWORD
        value: "${{ secret(32) }}"
  - name: web
    image: nginxinc/nginx-unprivileged@sha256:...
    port: 8080                   # a public web app's HTTP port
    healthCheckPath: /           # optional
    replicas: 1
    env:
      - name: REDIS_URL
        value: "redis://:${{ apps.redis.env.REDIS_PASSWORD }}@${{ apps.redis.host }}:${{ apps.redis.port }}/0"
```

Unknown fields are refused, so a template cannot ask for anything the format has no word for (host paths, privileges, other namespaces). Every app is checked by the same rules as an app created through the API (names, images, ports, disk mount paths, arguments, variable names and sizes).

### Values

A variable's value is text with `${{ ... }}` expressions:

| Expression | Resolves to |
|------------|-------------|
| `${{ secret(n) }}` | n random characters (16-128, letters and digits, from `crypto/rand`), drawn once per occurrence on deploy |
| `${{ db.VAR }}` | the project's database: `DATABASE_URL`, `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` or `PGPASSWORD`. Must be the whole value; stored as a reference and resolved at each deploy, like a reference made in Studio |
| `${{ apps.<name>.host }}` | the app's host name inside the project (its name) |
| `${{ apps.<name>.port }}` | an internal service's first internal port, or 80 (the Service's HTTP port) for a public web app |
| `${{ apps.<name>.env.VAR }}` | the value another app's variable resolves to; that variable may not itself read another app's variable or the database |

A value holding anything generated (`secret(n)`, or another app's generated value) is stored as a **secret** of the app: in the vault under the app's own prefix, never in the app record, the template, a response, the log or the Deployment spec (the pod gets it from the app's Secret). Everything else is a plain value. `args` may name a variable as `$(VAR)`; Kubernetes fills it in from the container's environment, so a password passed on the command line is not written into the spec either.

## Deploy: all or nothing

Before anything is created, the deploy checks, and refuses with **409** listing every reason:

- the plan's app count: apps held + apps in the template ≤ the plan's `maxApps` (FREE 2, STANDARD 5, ENTERPRISE 20);
- no app name the project already holds;
- each disk within the plan's app-disk cap;
- the project's database is ready, if a value references it;
- a vault exists, if the template generates secrets;
- the private network rule (below);
- CPU and memory: all the template's apps together fit the cluster at the plan's size (the same admission a single deploy uses, counting other apps' rollouts in flight), on a node that runs the app sandbox;
- the platform storage budget holds the template's disks together.

Then, in order: turn on the private network (if needed), store each app's generated secrets and create the app, then deploy each app. If any step fails, what the deploy made is removed in reverse (apps with their workloads, disks and secrets, then the private network it turned on) and the answer names the step that failed: 409 when the project's state refused it (a name or app slot taken meanwhile, a deploy refused for room or plan), 500 when the platform failed (vault, store, cluster). The rollback waits up to about 30 seconds for another operation holding an app or the project. If the removal itself cannot finish, the answer is 500 naming what is left. One template deploy runs at a time per project; it does not lock the project against a pause or deletion started meanwhile.

The deploy is all or nothing up to the point where every app's workload has been applied. A rollout that later fails (for example an image that crashes) shows on that app like any failed deploy and is not removed, so its logs can be read.

## Private network

A template needs the project's private network (EXC-524) when it has an internal service or one app reaches another by `host` or `port`. If the network is off, the deploy turns it on, and that needs:

- an org admin or owner (the same rule as `PUT .../app-network`; a developer gets **403**), and
- an explicit `{"confirmPrivateNetwork": true}` in the body (without it: 409 with `"code": "confirm_private_network"`). Studio asks for this confirmation.

A template whose apps only share a generated value leaves the network alone.
