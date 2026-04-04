interface ScriptMetadata {
  id: string;
  worker: Worker;
  createdAt: Date;
  invocations: number;
  lastInvoked?: Date;
}

const RUNTIME_SECRET = Deno.env.get("RUNTIME_SECRET");
if (!RUNTIME_SECRET) {
  console.error("FATAL: RUNTIME_SECRET environment variable is required");
  Deno.exit(1);
}
const MAX_CODE_SIZE = 512 * 1024; // 512 KB
const MAX_INVOKE_SIZE = 1024 * 1024; // 1 MB
// Allowed network hosts for workers — only project Postgres services
// Format: "host1:port1,host2:port2" or empty for no network access
const ALLOWED_HOSTS = (Deno.env.get("ALLOWED_HOSTS") || "").split(",").filter(Boolean);

class DenoScriptServer {
  private scripts = new Map<string, ScriptMetadata>();
  private maxScripts = 100; // reduced from 500

  async deploy(scriptId: string, code: string): Promise<{ id: string; url: string }> {
    if (this.scripts.size >= this.maxScripts) {
      throw new Error(`Max scripts limit reached (${this.maxScripts})`);
    }
    if (!scriptId || scriptId.length > 64 || !/^[a-zA-Z0-9_\-]+$/.test(scriptId)) {
      throw new Error("Invalid script ID");
    }
    if (!code || code.length > MAX_CODE_SIZE) {
      throw new Error(`Code exceeds maximum size (${MAX_CODE_SIZE / 1024} KB)`);
    }

    // Terminate existing worker if redeploying
    if (this.scripts.has(scriptId)) {
      this.scripts.get(scriptId)!.worker.terminate();
    }

    const workerCode = `
      ${code}

      self.onmessage = async (e) => {
        try {
          const { type, data } = e.data;
          if (type === 'invoke') {
            let result;
            if (typeof handler !== 'undefined') {
              result = await handler(data);
            } else {
              throw new Error('No handler function exported');
            }
            self.postMessage({ type: 'success', result });
          }
        } catch (error) {
          self.postMessage({ type: 'error', error: error.message });
        }
      };
      self.postMessage({ type: 'ready' });
    `;

    const blob = new Blob([workerCode], { type: "application/javascript" });
    // Security: restrict net to allowed hosts only (project Postgres)
    // If no hosts configured, disable network entirely
    const netPermission: boolean | string[] = ALLOWED_HOSTS.length > 0 ? ALLOWED_HOSTS : false;
    const worker = new Worker(URL.createObjectURL(blob), {
      type: "module",
      deno: { permissions: { net: netPermission, env: false, read: false, write: false, run: false, ffi: false } }
    });

    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("Worker init timeout")), 5000);
      worker.onmessage = (e) => { if (e.data.type === 'ready') { clearTimeout(timeout); resolve(); } };
      worker.onerror = (err) => { clearTimeout(timeout); reject(err); };
    });

    this.scripts.set(scriptId, { id: scriptId, worker, createdAt: new Date(), invocations: 0 });
    console.log(`Deployed: ${scriptId}`);
    return { id: scriptId, url: `/invoke/${scriptId}` };
  }

  async invoke(scriptId: string, data: any): Promise<any> {
    const script = this.scripts.get(scriptId);
    if (!script) throw new Error(`Script not found: ${scriptId}`);

    script.invocations++;
    script.lastInvoked = new Date();

    return new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        // Terminate worker on timeout to prevent CPU spinning
        script.worker.terminate();
        this.scripts.delete(scriptId);
        reject(new Error("Execution timeout (30s) — function terminated"));
      }, 30000);
      script.worker.onmessage = (e) => {
        clearTimeout(timeout);
        if (e.data.type === 'success') resolve(e.data.result);
        else reject(new Error(e.data.error));
      };
      script.worker.onerror = (err) => { clearTimeout(timeout); reject(new Error("Worker error")); };
      script.worker.postMessage({ type: 'invoke', data });
    });
  }

  delete(scriptId: string): boolean {
    const script = this.scripts.get(scriptId);
    if (script) { script.worker.terminate(); this.scripts.delete(scriptId); return true; }
    return false;
  }

  list() {
    return Array.from(this.scripts.values()).map(s => ({
      id: s.id, invocations: s.invocations,
      uptime: Date.now() - s.createdAt.getTime(),
      lastInvoked: s.lastInvoked?.toISOString()
    }));
  }

  getStats() {
    return { totalScripts: this.scripts.size, maxScripts: this.maxScripts, scripts: this.list() };
  }
}

const server = new DenoScriptServer();

Deno.serve({ port: 8000 }, async (req: Request) => {
  const url = new URL(req.url);
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
  };

  if (req.method === "OPTIONS") return new Response(null, { status: 204, headers });

  // Authenticate all requests with shared secret (except health)
  if (url.pathname !== "/health") {
    if (req.headers.get("X-Runtime-Secret") !== RUNTIME_SECRET) {
      return Response.json({ error: "forbidden" }, { status: 403, headers });
    }
  }

  try {
    if (url.pathname === "/health") {
      return Response.json({ status: "healthy", scripts: server.getStats().totalScripts, uptime: performance.now() }, { headers });
    }

    if (url.pathname === "/deploy" && req.method === "POST") {
      const body = await req.text();
      if (body.length > MAX_CODE_SIZE + 4096) {
        return Response.json({ error: "Payload too large" }, { status: 413, headers });
      }
      const { id, code } = JSON.parse(body);
      if (!id || !code) return Response.json({ error: "Missing id or code" }, { status: 400, headers });
      const result = await server.deploy(id, code);
      return Response.json(result, { status: 201, headers });
    }

    if (url.pathname.startsWith("/invoke/")) {
      const scriptId = decodeURIComponent(url.pathname.split("/invoke/")[1]);
      if (!scriptId || !/^[a-zA-Z0-9_\-]+$/.test(scriptId)) {
        return Response.json({ error: "Invalid script ID" }, { status: 400, headers });
      }
      const body = await req.text();
      if (body.length > MAX_INVOKE_SIZE) {
        return Response.json({ error: "Payload too large" }, { status: 413, headers });
      }
      let data;
      try { data = JSON.parse(body); } catch { data = { body }; }
      const result = await server.invoke(scriptId, data);
      if (result && typeof result === 'object' && result.status && result.body) {
        return new Response(result.body, { status: result.status, headers: result.headers || headers });
      }
      return Response.json(result, { headers });
    }

    if (url.pathname.startsWith("/delete/") && req.method === "DELETE") {
      const scriptId = decodeURIComponent(url.pathname.split("/delete/")[1]);
      if (!scriptId || !/^[a-zA-Z0-9_\-]+$/.test(scriptId)) {
        return Response.json({ error: "Invalid script ID" }, { status: 400, headers });
      }
      return server.delete(scriptId)
        ? Response.json({ status: "deleted", id: scriptId }, { headers })
        : Response.json({ error: "Not found" }, { status: 404, headers });
    }

    if (url.pathname === "/scripts") return Response.json({ scripts: server.list() }, { headers });
    if (url.pathname === "/stats") return Response.json(server.getStats(), { headers });

    return Response.json({ error: "Not found" }, { status: 404, headers });
  } catch (error: any) {
    // Don't leak stack traces — only return message, truncated
    const msg = String(error?.message || "Internal error").slice(0, 500);
    return Response.json({ error: msg }, { status: 500, headers });
  }
});

console.log("Deno Edge Functions runtime on :8000");
