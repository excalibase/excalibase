// An app's name is its address inside the project (EXC-524): a DNS label that
// is not one of the Service names the platform keeps in the namespace.
const APP_NAME = /^[a-z][a-z0-9-]{0,48}[a-z0-9]$/;
const RESERVED = new Set(['deno-runtime']);

export function isValidAppName(name: string): boolean {
  return APP_NAME.test(name) && !name.startsWith('proj-') && !RESERVED.has(name);
}
