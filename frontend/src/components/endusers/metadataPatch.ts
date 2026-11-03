// A JSON merge patch from one metadata object to another: changed and added
// keys carry their new value, removed keys are sent as null.
export function metadataMergePatch(
  before: Record<string, unknown>,
  after: Record<string, unknown>,
): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(after)) {
    if (!Object.hasOwn(before, key) || JSON.stringify(before[key]) !== JSON.stringify(value)) patch[key] = value;
  }
  for (const key of Object.keys(before)) {
    if (!Object.hasOwn(after, key)) patch[key] = null;
  }
  return patch;
}

export function parseMetadataObject(text: string): { value?: Record<string, unknown>; error?: string } {
  if (text.trim() === '') return { value: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return { error: 'This is not valid JSON.' };
  }
  if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { error: 'Metadata must be a JSON object, like {"plan": "pro"}.' };
  }
  return { value: parsed as Record<string, unknown> };
}
