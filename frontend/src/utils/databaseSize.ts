import { formatBytes } from './formatBytes';

// The customer's data size as the server reports it; a DocumentDB project's
// documents are included (EXC-531). Undefined when nothing is reported.
export function databaseSize(metrics: { databaseSizeBytes?: number | null }): string | undefined {
  if (metrics.databaseSizeBytes == null) return undefined;
  return formatBytes(metrics.databaseSizeBytes);
}
