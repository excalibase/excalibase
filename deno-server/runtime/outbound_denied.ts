// Deno's refusal names the blocked host:port but tells the author to pass a
// CLI flag they cannot pass; say where the project's allowlist is instead.
// Kept free of outer references: the worker prelude embeds its source.
export function outboundDeniedMessage(message: string): string | null {
  const blocked = /^Requires net access to "([^"]+)"/.exec(message);
  if (!blocked) return null;
  return `outbound host ${blocked[1]} isn't allowed for this project; add it in Studio → Functions → Outbound hosts`;
}
