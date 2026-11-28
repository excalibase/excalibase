export type EnvEntry = { key: string; value: string };

// parseEnv turns a .env-style blob into entries. Handles:
//   - blank lines, full-line comments (#)
//   - optional `export ` prefix
//   - single- and double-quoted values (inline literal, no escape expansion)
// Invalid lines are returned as errors instead of throwing.
export function parseEnv(src: string): { entries: EnvEntry[]; errors: string[] } {
  const entries: EnvEntry[] = [];
  const errors: string[] = [];
  const lines = src.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line || line.startsWith('#')) continue;
    const bare = line.replace(/^export\s+/, '');
    const eqIdx = bare.indexOf('=');
    if (eqIdx <= 0) {
      errors.push(`line ${i + 1}: missing '='`);
      continue;
    }
    const key = bare.slice(0, eqIdx).trim();
    let value = bare.slice(eqIdx + 1).trim();
    if (
      (value.startsWith('"') && value.endsWith('"') && value.length >= 2) ||
      (value.startsWith("'") && value.endsWith("'") && value.length >= 2)
    ) {
      value = value.slice(1, -1);
    }
    if (!/^[A-Z][A-Z0-9_]*$/.test(key)) {
      errors.push(`line ${i + 1}: invalid key "${key}" (must be UPPER_SNAKE)`);
      continue;
    }
    entries.push({ key, value });
  }
  return { entries, errors };
}

// A non-2xx answer the function itself gave (marked by the server) is its
// output, shown as status and body rather than as a platform error.
export function functionAnswer(err: unknown): string | null {
  const response = (err as { response?: { status?: number; data?: unknown; headers?: Record<string, string> } }).response;
  if (response?.headers?.['x-excalibase-function-response'] !== '1') return null;
  const body = typeof response.data === 'string' ? response.data : JSON.stringify(response.data, null, 2);
  return `HTTP ${response.status}\n${body ?? ''}`;
}
