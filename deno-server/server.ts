interface ScriptMetadata {
  id: string;
  worker: Worker;
  createdAt: Date;
  invocations: number;
  lastInvoked?: Date;
}

class DenoScriptServer {
  private scripts = new Map<string, ScriptMetadata>();
  private maxScripts = 500;

  async deploy(scriptId: string, code: string): Promise<{ id: string; url: string }> {
    if (this.scripts.size >= this.maxScripts) {
      throw new Error(`Max scripts limit reached (${this.maxScripts})`);
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
          self.postMessage({ type: 'error', error: error.message, stack: error.stack });
        }
      };
      self.postMessage({ type: 'ready' });
    `;

    const blob = new Blob([workerCode], { type: "application/javascript" });
    const worker = new Worker(URL.createObjectURL(blob), {
      type: "module",
      deno: { permissions: { net: true, env: false, read: false, write: false, run: false, ffi: false } }
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
      const timeout = setTimeout(() => reject(new Error("Execution timeout (30s)")), 30000);
      script.worker.onmessage = (e) => {
        clearTimeout(timeout);
        if (e.data.type === 'success') resolve(e.data.result);
        else reject(new Error(e.data.error));
      };
      script.worker.onerror = (err) => { clearTimeout(timeout); reject(err); };
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
  const headers = {
    "Content-Type": "application/json",
    "Access-Control-Allow-Origin": "*",
    "Access-Control-Allow-Methods": "GET, POST, DELETE, OPTIONS",
    "Access-Control-Allow-Headers": "Content-Type"
  };

  if (req.method === "OPTIONS") return new Response(null, { status: 204, headers });

  try {
    if (url.pathname === "/health") {
      return Response.json({ status: "healthy", scripts: server.getStats().totalScripts, uptime: performance.now() }, { headers });
    }

    if (url.pathname === "/deploy" && req.method === "POST") {
      const { id, code } = await req.json();
      if (!id || !code) return Response.json({ error: "Missing id or code" }, { status: 400, headers });
      const result = await server.deploy(id, code);
      return Response.json(result, { status: 201, headers });
    }

    if (url.pathname.startsWith("/invoke/")) {
      const scriptId = url.pathname.split("/invoke/")[1];
      let data;
      if (req.headers.get("content-type")?.includes("application/json")) {
        data = await req.json();
      } else {
        data = { method: req.method, url: req.url, headers: Object.fromEntries(req.headers), body: await req.text() };
      }
      const result = await server.invoke(scriptId, data);
      if (result && typeof result === 'object' && result.status && result.body) {
        return new Response(result.body, { status: result.status, headers: result.headers || headers });
      }
      return Response.json(result, { headers });
    }

    if (url.pathname.startsWith("/delete/") && req.method === "DELETE") {
      const scriptId = url.pathname.split("/delete/")[1];
      return server.delete(scriptId)
        ? Response.json({ status: "deleted", id: scriptId }, { headers })
        : Response.json({ error: "Not found" }, { status: 404, headers });
    }

    if (url.pathname === "/scripts") return Response.json({ scripts: server.list() }, { headers });
    if (url.pathname === "/stats") return Response.json(server.getStats(), { headers });

    return Response.json({ error: "Not found" }, { status: 404, headers });
  } catch (error: any) {
    return Response.json({ error: error.message }, { status: 500, headers });
  }
});

console.log("Deno Edge Functions runtime on :8000");
