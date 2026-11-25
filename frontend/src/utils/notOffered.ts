// An install that does not mount a service answers its routes with 404. The
// project itself is known to exist by then, so the 404 means "not offered" and
// is not a failure to load.
export function isNotOffered(error: unknown): boolean {
  return (error as { response?: { status?: number } } | null)?.response?.status === 404;
}
