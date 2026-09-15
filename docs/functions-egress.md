# Edge function egress — the outbound allowlist

Edge functions run in a Deno worker with **no network access by default**.
`fetch()` to any host fails with `NotCapable` until the project opts a host
in. This page covers how to allow outbound calls, what the allowlist accepts,
and how the setting reaches the worker.

## Enable egress for a project

```bash
# Developer+ on the project's org (same gate as deploying a function)
curl -sf -X PUT -H "Authorization: Bearer $PAT" -H "Content-Type: application/json" \
  "https://<host>/api/projects/<projectId>/functions/egress" \
  -d '{"allowedHosts": ["api.stripe.com", "*.amazonaws.com", "hooks.example.com:8443"]}'
```

```json
{
  "allowedHosts":   ["*.amazonaws.com", "api.stripe.com", "hooks.example.com:8443"],
  "defaultHosts":   [],
  "effectiveHosts": ["*.amazonaws.com", "api.stripe.com", "hooks.example.com:8443"]
}
```

* `allowedHosts` — the project's own setting; `PUT` replaces it whole.
  Send `[]` to switch egress off again.
* `defaultHosts` — the operator-level list every project gets
  (`EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS`, read-only here).
* `effectiveHosts` — the union actually rendered into the runtime.

`GET` on the same path reads it back. The change takes effect on the next
invocation: the platform re-renders the project's runtime and redeploys every
function of the project with the new permission (see below).

Then, in a function:

```ts
export default async (req: Request) => {
  const r = await fetch("https://api.stripe.com/v1/charges", { /* … */ });
  return new Response(await r.text());
};
```

## What an entry can be

The list is handed to the worker's Deno `net` permission verbatim, so it takes
Deno's `--allow-net` grammar and nothing looser:

| Entry | Meaning |
|---|---|
| `api.stripe.com` | that host, any port |
| `api.stripe.com:8443` | that host, that port only |
| `*.amazonaws.com` | any name strictly below the suffix (`s3.amazonaws.com`, not `amazonaws.com`) |
| `*.amazonaws.com:443` | the same, one port |
| `203.0.113.10`, `[2606:4700::1111]:443` | a public IP literal, optional port |

Entries are lower-cased, de-duplicated and sorted on write. At most 64 per
project.

Refused with `400` and the offending entry named:

* a bare `*`, `*` anywhere but as a leading `*.`, or a single-label suffix (`*.com`)
* a scheme, path, query, or comma (`https://x`, `x/v1`, `a,b`) — one host per entry
* a CIDR (`203.0.113.0/24`) — Deno permissions do not take ranges
* an invalid port (`:0`, `:65536`, `:https`)
* a single-label hostname (`localhost`) or a cluster-internal suffix
  (`.svc.cluster.local`, `.internal`, `.local`, `.localhost`, `.home.arpa`)
* any non-public IP literal: loopback, RFC-1918, link-local (the cloud
  metadata endpoint `169.254.169.254`), CGNAT, ULA, IPv4-mapped/NAT64/6to4
  forms of those, unspecified, multicast

A hostname that *resolves* to a private address is not caught by the
literal check; on Kubernetes the NetworkPolicy below refuses those ranges at
the packet level regardless of what the name resolves to.

## How the setting reaches the worker

```
PUT /functions/egress
   │  edgefn.ParseEgressHosts (validate + canonicalise)
   ▼
edge_function_settings.egress_allowed_hosts   (platform Postgres)
   │  merged with EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS  → effective list
   ├─► k8s: deno-runtime Deployment env ALLOWED_HOSTS=<list>
   │        + pod-template annotation excalibase.io/egress-hash → rollout
   │        + NetworkPolicy deno-runtime-egress re-rendered in lock-step
   ├─► every deploy payload carries allowedHosts (first deploy, redeploy, replay)
   ▼
Deno runtime: worker `net` permission = union(ALLOWED_HOSTS env, deploy.allowedHosts)
```

**Kubernetes (per-project runtime pod).** The effective list is rendered as
the pod's `ALLOWED_HOSTS` env. A change updates the Deployment (env + hash
annotation), which rolls the pod; the runtime replayer (EXC-337) pushes the
project's functions back into the fresh pod. The pod's egress
`NetworkPolicy` is updated in the same call: with an empty list it allows
only DNS, the project's own Postgres and the provisioning API; with a
non-empty list it additionally opens public IPv4 space
(`0.0.0.0/0` except loopback, RFC-1918, link-local, CGNAT, this-network) on
the TCP ports the list names (443 for entries without a port). A
NetworkPolicy cannot match hostnames, so the per-host part is enforced by
the Deno permission; the policy fences the address space and ports around it
so that an isolate escape still cannot reach the metadata endpoint, Vault,
the platform database or another tenant. Requires a policy-enforcing CNI
(Calico / Cilium); with the default minikube CNI the object exists but is
not enforced.

**Docker (shared runtime).** There is one runtime container for the whole
install and provisioning does not manage it, so the setting cannot be
rendered as its env. Instead every deploy payload carries `allowedHosts`
and the runtime grants it to that function's worker only — sibling projects
on the same runtime keep their own lists. The container's own
`ALLOWED_HOSTS` env acts as an operator baseline that is unioned in.

**Default.** No row, `[]`, and no operator default all mean the worker gets
`net: false` — no network at all. Egress is never opened implicitly.

## Operator default

```
EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS=*.excalibase.io,api.resend.com
```

Same grammar and validation as the per-project list; a malformed value
stops provisioning at boot. It is merged (union) into every project's
effective list and shown as `defaultHosts`. Projects cannot remove a default
entry — it is the operator's floor, not a suggestion.
