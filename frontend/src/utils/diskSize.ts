export type DiskUnit = 'Mi' | 'Gi';

const MI = 1024 ** 2;
const GI = 1024 ** 3;
const TI = 1024 ** 4;
// The server's floor for a disk sized in mebibytes.
export const MIN_DISK_MI = 64;

const UNIT_BYTES: Record<DiskUnit, number> = { Mi: MI, Gi: GI };
const SIZE = /^(\d+)(Mi|Gi)$/;

const trimmed = (value: number) => String(Math.round(value * 10) / 10);

// Reads in the same Mi/Gi units the server uses in sizes and messages.
export function formatDiskBytes(bytes: number): string {
  if (bytes >= TI) return `${trimmed(bytes / TI)}Ti`;
  if (bytes >= GI) return `${trimmed(bytes / GI)}Gi`;
  if (bytes > 0 && bytes < MI / 2) return '<1Mi';
  return `${Math.round(bytes / MI)}Mi`;
}

export function splitDiskSize(size: string): { amount: string; unit: DiskUnit } {
  const match = SIZE.exec(size);
  return match ? { amount: match[1], unit: match[2] as DiskUnit } : { amount: '', unit: 'Gi' };
}

export function sizeToBytes(size: string): number | null {
  const match = SIZE.exec(size);
  return match ? Number(match[1]) * UNIT_BYTES[match[2] as DiskUnit] : null;
}

export function toDiskSize(amount: string, unit: DiskUnit): { size: string; bytes: number } | null {
  if (!/^\d+$/.test(amount)) return null;
  const count = Number(amount);
  if (count < (unit === 'Mi' ? MIN_DISK_MI : 1)) return null;
  return { size: `${count}${unit}`, bytes: count * UNIT_BYTES[unit] };
}
