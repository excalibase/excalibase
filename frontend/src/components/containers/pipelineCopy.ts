import type { Deploy } from '../../api/apps';

// The first twelve hex characters, as registries and docker show a digest.
export function shortDigest(digest?: string): string {
  if (!digest) return '';
  return digest.slice(digest.indexOf(':') + 1, digest.indexOf(':') + 13);
}

export function deploySourceLabel(deploy: Deploy): string {
  switch (deploy.source) {
    case 'studio':
      return 'Studio';
    case 'api':
      return deploy.commitSha ? `CI · ${deploy.commitSha.slice(0, 7)}` : 'API';
    case 'image-watcher':
      return 'Image watcher';
    default:
      return 'Not recorded';
  }
}

// How long the deploy took to finish; empty while it runs.
export function deployDuration(deploy: Deploy): string {
  if (!deploy.finishedAt) return '';
  const seconds = Math.max(
    0,
    Math.round((Date.parse(deploy.finishedAt) - Date.parse(deploy.createdAt)) / 1000),
  );
  if (Number.isNaN(seconds)) return '';
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`;
}

// What the deploy ran, as the caller named it with the digest it resolved to.
export function deployImageLabel(deploy: Deploy): string {
  if (deploy.imageRef && deploy.digest) return `${deploy.imageRef} → ${shortDigest(deploy.digest)}`;
  return deploy.image;
}
