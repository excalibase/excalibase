// The platform as a user and their coding tool reach it: Studio's API with the operator's session,
// and MCP with a project-bound token. Every call fails loudly with what the platform answered.

export function studio(base, token) {
  async function call(method, path, body) {
    const answer = await fetch(`${base}/api${path}`, {
      method,
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(600_000),
    });
    const text = await answer.text();
    if (!answer.ok) throw new Error(`${method} ${path}: ${answer.status} ${text.slice(0, 400)}`);
    return text ? JSON.parse(text) : null;
  }
  return {
    get: (path) => call('GET', path),
    post: (path, body) => call('POST', path, body ?? {}),
    put: (path, body) => call('PUT', path, body),
  };
}

export async function signIn(base, username, password) {
  const answer = await fetch(`${base}/api/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  });
  const body = await answer.json().catch(() => ({}));
  if (!answer.ok || !body.token) throw new Error(`sign-in as ${username}: ${answer.status}`);
  return body.token;
}

let rpcId = 0;

/** A minimal MCP client over streamable HTTP, as a coding tool speaks it. */
export function mcp(url, token) {
  return async function tool(name, args = {}) {
    const answer = await fetch(url, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
        Accept: 'application/json, text/event-stream',
      },
      body: JSON.stringify({ jsonrpc: '2.0', id: ++rpcId, method: 'tools/call', params: { name, arguments: args } }),
      signal: AbortSignal.timeout(600_000),
    });
    const text = await answer.text();
    const line = text.startsWith('{') ? text : text.split('\n').find((l) => l.startsWith('data:'))?.slice(5);
    if (!line) throw new Error(`${name}: ${answer.status} ${text.slice(0, 400)}`);
    const reply = JSON.parse(line);
    if (reply.error) throw new Error(`${name}: ${reply.error.message}`);
    const content = reply.result?.content?.[0]?.text ?? '';
    if (reply.result?.isError) throw new Error(`${name}: ${content.slice(0, 600)}`);
    try {
      return JSON.parse(content);
    } catch {
      return content;
    }
  };
}

export const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** Polls check until it returns a truthy value; the last value and error are in the failure. */
/** Retries check until it returns a truthy value; a thrown error is retried unless marked fatal. */
export async function eventually(what, check, { tries = 60, every = 3000 } = {}) {
  let last;
  for (let i = 0; i < tries; i++) {
    try {
      last = await check();
      if (last) return last;
    } catch (error) {
      if (error.fatal) throw error;
      last = error.message;
    }
    await sleep(every);
  }
  throw new Error(`${what}: still ${typeof last === 'string' ? last : JSON.stringify(last)?.slice(0, 400)}`);
}
