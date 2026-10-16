// A site name is the first label of its hostname; the same rules as the
// server's sites.ValidateName, so Studio refuses what the server would.
const NAME = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;

const RESERVED = new Set([
  'www',
  'api',
  'admin',
  'studio',
  'mail',
  'status',
  'docs',
  'dashboard',
  'console',
  'support',
  'billing',
  'login',
  'ns',
  'ns1',
  'ns2',
  'mx',
  'smtp',
  'imap',
  'pop',
  'webmail',
  'autodiscover',
  'autoconfig',
  'static',
  'assets',
  'cdn',
  'sso',
  'oauth',
  'auth',
  'id',
  'account',
  'accounts',
  'security',
  'help',
  'blog',
  'graphql',
  'rest',
  'realtime',
  'storage',
  'functions',
  'edge',
  'gateway',
  'internal',
  'staging',
  'dev',
  'beta',
  'app',
  'apps',
]);

// Why the name would be refused, or null when the server would accept it.
export function siteNameProblem(name: string): string | null {
  if (!NAME.test(name)) {
    return 'A site name is 3 to 63 lowercase letters, digits or hyphens, starting and ending with a letter or digit.';
  }
  if (name.includes('--')) return 'A site name may not contain two hyphens in a row.';
  if (RESERVED.has(name) || name.includes('excalibase')) {
    return `"${name}" is reserved; choose another site name.`;
  }
  return null;
}
