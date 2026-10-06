// The server's rule for a new account's name (handler/helpers.go validUsername).
export const USERNAME_PATTERN = /^[A-Za-z0-9_]{3,32}$/;
export const USERNAME_RULE = 'Use 3–32 letters, numbers or underscores';

export function usernameError(username: string): string | undefined {
  return USERNAME_PATTERN.test(username) ? undefined : USERNAME_RULE;
}

// Postgres keeps 63 bytes of a name and silently drops the rest; the server refuses longer.
export const MAX_IDENTIFIER_BYTES = 63;

export function identifierBytes(name: string): number {
  return new TextEncoder().encode(name).length;
}

export function identifierError(kind: string, name: string): string | undefined {
  const bytes = identifierBytes(name);
  return bytes > MAX_IDENTIFIER_BYTES
    ? `${kind} names can be at most ${MAX_IDENTIFIER_BYTES} characters; this one has ${bytes}`
    : undefined;
}
