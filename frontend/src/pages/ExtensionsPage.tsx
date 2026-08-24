import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, Search, Package, Check, X } from 'lucide-react';
import { useExtensions, useCreateExtension, useDropExtension } from '../hooks/useSchema';
import { ConfirmModal } from '../components/ui/ConfirmModal';

export function ExtensionsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: extensions = [], isLoading } = useExtensions(projectId || '');
  const createExt = useCreateExtension(projectId || '');
  const dropExt = useDropExtension(projectId || '');
  const [search, setSearch] = useState('');
  const [dropTarget, setDropTarget] = useState<string | null>(null);

  const filtered = extensions.filter(e =>
    e.name.toLowerCase().includes(search.toLowerCase()) ||
    (e.comment?.toLowerCase().includes(search.toLowerCase()))
  );

  const installed = filtered.filter(e => e.installedVersion);
  const available = filtered.filter(e => !e.installedVersion);

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="extensions-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Extensions</h3>
        <div className="relative">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-tertiary" />
          <input
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search extensions..."
            className="pl-9 pr-4 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm w-64 focus:outline-none focus:ring-2 focus:ring-purple-500"
            data-testid="ext-search"
          />
        </div>
      </div>

      {/* Installed */}
      {installed.length > 0 && (
        <div className="mb-6">
          <h4 className="text-sm font-medium text-text-secondary mb-2">Installed ({installed.length})</h4>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {installed.map(ext => (
              <div key={ext.name} className="rounded-lg border border-green-500/30 bg-green-500/5 p-4" data-testid={`ext-${ext.name}`}>
                <div className="flex items-start justify-between">
                  <div className="flex items-center gap-2">
                    <Package className="w-4 h-4 text-green-400" />
                    <span className="text-sm font-medium text-text-primary">{ext.name}</span>
                  </div>
                  <button
                    onClick={() => setDropTarget(ext.name)}
                    className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
                    data-testid={`disable-ext-${ext.name}`}
                  >
                    <X className="w-4 h-4" />
                  </button>
                </div>
                <p className="text-xs text-text-tertiary mt-1 line-clamp-2">{ext.comment || 'No description'}</p>
                <div className="flex items-center gap-2 mt-2">
                  <Check className="w-3 h-3 text-green-400" />
                  <span className="text-xs text-green-400">v{ext.installedVersion}</span>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Available */}
      {available.length > 0 && (
        <div>
          <h4 className="text-sm font-medium text-text-secondary mb-2">Available ({available.length})</h4>
          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-3">
            {available.map(ext => (
              <div key={ext.name} className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid={`ext-${ext.name}`}>
                <div className="flex items-start justify-between">
                  <div className="flex items-center gap-2">
                    <Package className="w-4 h-4 text-text-tertiary" />
                    <span className="text-sm font-medium text-text-primary">{ext.name}</span>
                  </div>
                  <button
                    onClick={() => createExt.mutate({ name: ext.name })}
                    disabled={createExt.isPending}
                    className="px-2 py-1 text-xs font-medium text-purple-400 hover:bg-purple-500/10 rounded transition-colors"
                    data-testid={`enable-ext-${ext.name}`}
                  >
                    Enable
                  </button>
                </div>
                <p className="text-xs text-text-tertiary mt-1 line-clamp-2">{ext.comment || 'No description'}</p>
                <span className="text-xs text-text-tertiary mt-2 inline-block">v{ext.defaultVersion}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      <ConfirmModal
        open={!!dropTarget}
        onClose={() => setDropTarget(null)}
        onConfirm={() => { if (dropTarget) dropExt.mutate({ name: dropTarget, cascade: true }, { onSuccess: () => setDropTarget(null) }); }}
        title="Disable Extension"
        message={`Are you sure you want to disable "${dropTarget}"? This may affect dependent objects.`}
        confirmLabel="Disable"
        destructive
        loading={dropExt.isPending}
      />
    </div>
  );
}
