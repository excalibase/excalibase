import type { BackupInfo } from '../types';

const HIBERNATED_FAILURE = 'cannot backup a hibernated cluster';

function isHibernatedFailure(backup: { error?: string }): boolean {
  return (backup.error ?? '').includes(HIBERNATED_FAILURE);
}

export interface BackupView {
  readonly backups: readonly BackupInfo[];
  // Set only while paused: the newest completed backup.
  readonly newestCompleted?: BackupInfo;
  // True only when that backup is the one the pause took.
  readonly isPauseBackup: boolean;
}

// A paused database cannot be backed up, so the failures recorded for that
// reason are noise; any other failure is a real problem and stays.
export function backupView(listed: unknown, paused: boolean, pauseBackupId?: string): BackupView {
  const all: BackupInfo[] = Array.isArray(listed) ? listed : [];
  const backups = paused ? all.filter((b) => !(b.status === 'FAILED' && isHibernatedFailure(b))) : all;
  const newestCompleted = paused
    ? backups.filter((b) => b.status === 'COMPLETED').sort((a, b) => b.timestamp.localeCompare(a.timestamp))[0]
    : undefined;
  return { backups, newestCompleted, isPauseBackup: !!newestCompleted && newestCompleted.id === pauseBackupId };
}
