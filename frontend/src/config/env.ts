/**
 * Centralized accessor for Vite-injected env vars consumed by the frontend.
 *
 * Why a single module: scattering `import.meta.env.*` reads makes it easy to
 * miss defaults (or worse, leak `undefined` into a URL builder). Each getter
 * below returns a typed string with a sane fallback so call sites stay short.
 *
 * Add a matching line to `.env.example` and document it in README.md when
 * adding a new variable here.
 */

function readEnv(key: string): string {
  const value = (import.meta.env as Record<string, string | undefined>)[key];
  return typeof value === 'string' ? value : '';
}

/**
 * WebSocket URL for excalibase-graphql's CDC realtime endpoint. Defaults to
 * the local docker-compose stack used by the studio dev flow.
 */
export function graphqlRealtimeWsUrl(): string {
  return readEnv('VITE_EXCALIBASE_GRAPHQL_WS_URL') || 'ws://localhost:10000/api/v1/realtime';
}
