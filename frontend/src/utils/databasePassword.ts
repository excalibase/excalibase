export const PASSWORD_MIN = 16;
export const PASSWORD_MAX = 128;
const MIN_DISTINCT = 8;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u001f\u007f]/;

// Mirrors the server's rule for a chosen database password; blank asks for a
// generated one. Length alone makes a passphrase of several words qualify.
export function passwordProblem(password: string): string | null {
  if (password === '') return null;
  if (CONTROL.test(password)) return 'The password cannot contain control characters.';
  if (/^[=+\-@]/.test(password)) return 'The password cannot start with =, +, - or @.';
  if (password.length < PASSWORD_MIN) return `Use at least ${PASSWORD_MIN} characters, or leave it blank to generate one.`;
  if (password.length > PASSWORD_MAX) return `Use at most ${PASSWORD_MAX} characters.`;
  if (new Set(password).size < MIN_DISTINCT) return `Use at least ${MIN_DISTINCT} different characters.`;
  return null;
}
