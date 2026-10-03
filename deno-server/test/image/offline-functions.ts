// Run inside the built image with no network: a project's runtime has none
// unless its allowlist says so, so every module a v2 function is documented
// to import (the vendored lib, zod through the import map or by npm spec)
// must load from the image's own cache.
const functions: Record<string, string> = {
  bareZod: `import { mutation } from "npm:@excalibase/server@0.11.0";
import { z } from "zod";
var __excalibase_default = mutation({ args: z.object({ n: z.number() }), handler: async (_c, a) => ({ n: a.n }) });
export default __excalibase_default;`,
  npmZod: `import { mutation } from "npm:@excalibase/server@0.11.0";
import { z } from "npm:zod@^3.22.0";
var __excalibase_default = mutation({ args: z.object({ n: z.number() }), handler: async (_c, a) => ({ n: a.n }) });
export default __excalibase_default;`,
};
const headers = { "Content-Type": "application/json", "X-Runtime-Secret": Deno.env.get("RUNTIME_SECRET") ?? "" };
let failed = false;
for (const [id, code] of Object.entries(functions)) {
  const deploy = await fetch("http://localhost:8000/deploy", { method: "POST", headers, body: JSON.stringify({ id, code, secrets: {} }) });
  const invoke = await fetch(`http://localhost:8000/invoke/${id}`, {
    method: "POST", headers,
    body: JSON.stringify({ method: "POST", url: `/invoke/${id}`, headers: {}, body: JSON.stringify({ args: { n: 3 } }) }),
  });
  const answer = await invoke.json();
  const ok = deploy.status === 201 && answer.status === 200 && JSON.parse(answer.body).data?.n === 3;
  console.log(`${ok ? "ok" : "FAIL"} ${id}: deploy ${deploy.status}, invoke ${JSON.stringify(answer).slice(0, 200)}`);
  failed ||= !ok;
}
if (failed) Deno.exit(1);
