// The server's organization slug rule: 2–50 lowercase letters, digits and
// hyphens, starting with a letter or digit.
export const ORG_SLUG_MAX = 50;
const VALID_SLUG = /^[a-z0-9][a-z0-9-]{1,49}$/;

export const ORG_SLUG_RULE = `2–${ORG_SLUG_MAX} lowercase letters, digits and hyphens, starting with a letter or digit`;

// The slug a name suggests, cut to the cap without a dangling hyphen.
export function deriveOrgSlug(name: string): string {
  return name
    .toLowerCase()
    .replaceAll(/[^a-z0-9]+/g, '-')
    .replaceAll(/^-+/g, '')
    .slice(0, ORG_SLUG_MAX)
    .replaceAll(/-+$/g, '');
}

// Why the slug would be refused, or undefined when it would not.
export function orgSlugError(slug: string, name: string): string | undefined {
  const trimmed = slug.trim();
  if (trimmed === '') {
    if (name.trim() === '') return undefined;
    return `The name has no letters a–z or digits to build a slug from; enter a slug: ${ORG_SLUG_RULE}.`;
  }
  if (!VALID_SLUG.test(trimmed)) return `The slug must be ${ORG_SLUG_RULE}.`;
  return undefined;
}
