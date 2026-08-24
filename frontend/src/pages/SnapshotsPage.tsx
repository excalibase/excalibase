import { useState } from 'react';
import { useSnapshots, useExportSnapshot, useDeleteSnapshot } from '../hooks/useSnapshots';
import { useInstanceContext } from '../context/InstanceContext';
import { Camera, Download, Trash2, Plus, FileText } from 'lucide-react';
import { formatBytes } from '../utils/formatBytes';

function getSnapshotType(schemaOnly: boolean, dataOnly: boolean): string {
  if (schemaOnly) return 'Schema only';
  if (dataOnly) return 'Data only';
  return 'Full';
}

export function SnapshotsPage() {
  const { projectId } = useInstanceContext();

  const { data: snapshots = [], isLoading } = useSnapshots(projectId);
  const exportSnap = useExportSnapshot(projectId);
  const deleteSnap = useDeleteSnapshot(projectId);

  const [exportOpts, setExportOpts] = useState({ format: 'custom' as 'custom' | 'plain', schemaOnly: false, dataOnly: false });
  const [toast, setToast] = useState<{ msg: string; ok: boolean } | null>(null);

  function showToast(msg: string, ok: boolean) {
    setToast({ msg, ok });
    setTimeout(() => setToast(null), 4000);
  }

  function handleExport() {
    exportSnap.mutate(exportOpts, {
      onSuccess: (s) => showToast(`Snapshot ${s.snapshotId} created (${formatBytes(s.sizeBytes)})`, true),
      onError: (e: unknown) => {
        const msg = e instanceof Error ? e.message : String(e);
        showToast(msg || 'Export failed', false);
      },
    });
  }

  function handleDelete(snapshotId: string) {
    if (!confirm(`Delete snapshot ${snapshotId}?`)) return;
    deleteSnap.mutate(snapshotId, {
      onSuccess: () => showToast('Snapshot deleted', true),
      onError: (e: unknown) => {
        const msg = e instanceof Error ? e.message : String(e);
        showToast(msg || 'Delete failed', false);
      },
    });
  }

  const apiBase = `http://localhost:24005/api`;

  return (
    <div className="max-w-5xl mx-auto space-y-6">
      {toast && (
        <div className={`px-4 py-2 rounded-lg text-sm font-medium border ${toast.ok ? 'bg-green-900/20 text-green-400 border-green-500/30' : 'bg-red-900/20 text-red-400 border-red-500/30'}`}>
          {toast.msg}
        </div>
      )}

      {/* Export options */}
      <div className="bg-surface-card border border-border-primary rounded-xl p-5 space-y-4">
        <div className="flex items-center gap-4 flex-wrap">
          <div className="flex items-center gap-2">
            <label htmlFor="snapshot-format-select" className="text-xs text-text-secondary">Format:</label>
            <select
              id="snapshot-format-select"
              value={exportOpts.format}
              onChange={(e) => setExportOpts({ ...exportOpts, format: e.target.value as 'custom' | 'plain' })}
              className="px-2 py-1.5 bg-bg-secondary border border-border-primary rounded text-text-primary text-sm"
            >
              <option value="custom">Custom (pg_restore)</option>
              <option value="plain">Plain SQL</option>
            </select>
          </div>
          <label className="flex items-center gap-2 text-xs text-text-secondary cursor-pointer">
            <input type="checkbox" checked={exportOpts.schemaOnly} onChange={(e) => setExportOpts({ ...exportOpts, schemaOnly: e.target.checked, dataOnly: false })} className="accent-emerald-500" />
            <span>Schema only</span>
          </label>
          <label className="flex items-center gap-2 text-xs text-text-secondary cursor-pointer">
            <input type="checkbox" checked={exportOpts.dataOnly} onChange={(e) => setExportOpts({ ...exportOpts, dataOnly: e.target.checked, schemaOnly: false })} className="accent-emerald-500" />
            <span>Data only</span>
          </label>
          <button
            onClick={handleExport}
            disabled={!projectId || exportSnap.isPending}
            className="ml-auto inline-flex items-center gap-2 px-4 py-2 bg-accent-primary hover:bg-accent-primary-hover disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors"
          >
            <Plus className="w-4 h-4" />
            {exportSnap.isPending ? 'Exporting…' : 'Export Snapshot (pg_dump)'}
          </button>
        </div>
      </div>

      {/* Snapshots table */}
      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        <div className="px-6 py-4 border-b border-border-primary flex items-center gap-2">
          <Camera className="w-4 h-4 text-text-tertiary" />
          <span className="text-sm font-semibold text-text-primary">Snapshots</span>
          <span className="ml-auto text-xs text-text-tertiary">{snapshots.length} total</span>
        </div>

        {isLoading && (
          <div className="p-12 text-center text-text-secondary text-sm">Loading…</div>
        )}
        {!isLoading && snapshots.length === 0 && (
          <div className="p-12 text-center">
            <FileText className="w-12 h-12 mx-auto mb-3 text-text-tertiary" />
            <p className="text-text-secondary text-sm">No snapshots yet. Click "Export Snapshot" to create a pg_dump.</p>
          </div>
        )}
        {!isLoading && snapshots.length > 0 && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary text-left">
                <th className="px-6 py-3 font-medium">Snapshot ID</th>
                <th className="px-6 py-3 font-medium">Format</th>
                <th className="px-6 py-3 font-medium">Type</th>
                <th className="px-6 py-3 font-medium">Size</th>
                <th className="px-6 py-3 font-medium">Created</th>
                <th className="px-6 py-3 font-medium">Actions</th>
              </tr>
            </thead>
            <tbody>
              {snapshots.map((s) => (
                <tr key={s.snapshotId} className="border-b border-border-primary last:border-0 hover:bg-surface-hover transition-colors">
                  <td className="px-6 py-4 font-mono text-xs text-text-primary">{s.snapshotId}</td>
                  <td className="px-6 py-4 text-text-secondary">{s.format}</td>
                  <td className="px-6 py-4 text-text-tertiary text-xs">
                    {getSnapshotType(s.schemaOnly, s.dataOnly)}
                  </td>
                  <td className="px-6 py-4 text-text-secondary">{formatBytes(s.sizeBytes)}</td>
                  <td className="px-6 py-4 text-text-tertiary">{new Date(s.createdAt).toLocaleString()}</td>
                  <td className="px-6 py-4">
                    <div className="flex items-center gap-2">
                      <a
                        href={`${apiBase}/provision/${projectId}/snapshot/${s.snapshotId}/download`}
                        className="inline-flex items-center gap-1 px-2 py-1 bg-accent-primary/10 text-accent-primary hover:bg-accent-primary/20 rounded text-xs transition-colors"
                      >
                        <Download className="w-3 h-3" /> Download
                      </a>
                      <button
                        onClick={() => handleDelete(s.snapshotId)}
                        disabled={deleteSnap.isPending}
                        className="inline-flex items-center gap-1 px-2 py-1 bg-red-900/10 text-red-400 hover:bg-red-900/20 rounded text-xs transition-colors"
                      >
                        <Trash2 className="w-3 h-3" /> Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
