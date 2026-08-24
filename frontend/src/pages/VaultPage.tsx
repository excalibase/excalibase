import { useState, useEffect } from 'react';
import { KeyRound, Eye, EyeOff, Copy, Trash2, Loader2, Lock, Search, Check } from 'lucide-react';
import { useVaultSecretsList, useVaultSecret, useDeleteVaultSecret, useVaultStatus } from '../hooks/useVault';
import { ConfirmModal } from '../components/ui/ConfirmModal';

const PKI_PREFIX = 'pki/';

export function VaultPage() {
  const [search, setSearch] = useState('');
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [revealedPath, setRevealedPath] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);
  const [copiedField, setCopiedField] = useState<string | null>(null);

  const { data: status } = useVaultStatus();
  const { data: paths, isLoading: pathsLoading } = useVaultSecretsList();
  const { data: secretData, isLoading: secretLoading } = useVaultSecret(revealedPath);
  const deleteMutation = useDeleteVaultSecret();

  // Auto-hide revealed secret after 30s
  useEffect(() => {
    if (revealedPath) {
      const timer = setTimeout(() => {
        setRevealedPath(null);
        setSelectedPath(null);
      }, 30_000);
      return () => clearTimeout(timer);
    }
  }, [revealedPath]);

  // Filter out PKI paths and apply search
  const filteredPaths = (paths ?? [])
    .filter((p) => !p.startsWith(PKI_PREFIX))
    .filter((p) => search === '' || p.toLowerCase().includes(search.toLowerCase()));

  const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text);
    setCopiedField(field);
    setTimeout(() => setCopiedField(null), 2000);
  };

  if (status?.sealed) {
    return (
      <div data-testid="vault-page">
        <div className="flex flex-col items-center justify-center py-16 text-text-secondary">
          <Lock className="w-12 h-12 mb-4 text-text-tertiary" />
          <h3 className="text-lg font-medium text-text-primary mb-2">Vault is Sealed</h3>
          <p className="text-sm">The vault must be unsealed before secrets can be viewed.</p>
        </div>
      </div>
    );
  }

  const handleRowToggle = (path: string) => {
    setSelectedPath(selectedPath === path ? null : path);
    setRevealedPath(null);
  };

  return (
    <div data-testid="vault-page">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h3 className="text-lg font-semibold text-text-primary">Vault Secrets</h3>
          <p className="text-sm text-text-secondary mt-1">
            {filteredPaths.length} secret{filteredPaths.length === 1 ? '' : 's'}
          </p>
        </div>
      </div>

      {/* Search */}
      <div className="relative mb-4">
        <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-text-tertiary" />
        <input
          type="text"
          placeholder="Filter secrets..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full pl-10 pr-4 py-2 bg-surface-card border border-border-primary rounded-lg text-sm text-text-primary placeholder-text-tertiary focus:outline-none focus:ring-1 focus:ring-purple-500"
          data-testid="vault-search"
        />
      </div>

      {pathsLoading && (
        <div className="flex justify-center py-16">
          <Loader2 className="w-8 h-8 animate-spin text-purple-400" />
        </div>
      )}

      {!pathsLoading && filteredPaths.length === 0 && (
        <div className="text-center py-16 text-text-secondary text-sm">
          No secrets found.
        </div>
      )}

      {/* Secrets list */}
      <div className="space-y-1" data-testid="vault-secrets-list">
        {filteredPaths.map((path) => (
          <div
            key={path}
            className={`rounded-lg border transition-colors ${
              selectedPath === path
                ? 'border-purple-500/50 bg-purple-500/5'
                : 'border-border-primary bg-surface-card hover:border-border-secondary'
            }`}
          >
            {/* Path row */}
            <button
              type="button"
              className="w-full flex items-center justify-between px-4 py-3 text-left"
              onClick={() => handleRowToggle(path)}
              data-testid={`vault-secret-row-${path.replaceAll('/', '-')}`}
            >
              <div className="flex items-center gap-3 min-w-0">
                <KeyRound className="w-4 h-4 text-text-tertiary flex-shrink-0" />
                <span className="text-sm font-mono text-text-primary truncate">{path}</span>
              </div>
              <div className="flex items-center gap-2">
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    if (revealedPath === path) {
                      setRevealedPath(null);
                    } else {
                      setRevealedPath(path);
                      setSelectedPath(path);
                    }
                  }}
                  className="p-1.5 rounded hover:bg-surface-hover text-text-tertiary hover:text-text-primary transition-colors"
                  title={revealedPath === path ? 'Hide' : 'Reveal'}
                  data-testid={`vault-reveal-${path.replaceAll('/', '-')}`}
                >
                  {revealedPath === path ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
                </button>
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    setDeleteTarget(path);
                  }}
                  className="p-1.5 rounded hover:bg-red-500/10 text-text-tertiary hover:text-red-400 transition-colors"
                  title="Delete"
                  data-testid={`vault-delete-${path.replaceAll('/', '-')}`}
                >
                  <Trash2 className="w-4 h-4" />
                </button>
              </div>
            </button>

            {/* Expanded secret values */}
            {selectedPath === path && revealedPath === path && (
              <div className="px-4 pb-3 border-t border-border-primary">
                {secretLoading && (
                  <div className="flex justify-center py-4">
                    <Loader2 className="w-5 h-5 animate-spin text-purple-400" />
                  </div>
                )}
                {secretData && (
                  <div className="mt-3 space-y-2" data-testid="vault-secret-values">
                    {Object.entries(secretData).map(([key, value]) => (
                      <div key={key} className="flex items-center gap-3">
                        <span className="text-xs text-text-tertiary w-24 flex-shrink-0">{key}</span>
                        <span className="text-sm font-mono text-text-primary flex-1 truncate">
                          {key === 'password' ? '••••••••' : value}
                        </span>
                        <button
                          onClick={() => copyToClipboard(value, `${path}-${key}`)}
                          className="p-1 rounded hover:bg-surface-hover text-text-tertiary hover:text-text-primary"
                          title="Copy"
                        >
                          {copiedField === `${path}-${key}` ? (
                            <Check className="w-3.5 h-3.5 text-green-400" />
                          ) : (
                            <Copy className="w-3.5 h-3.5" />
                          )}
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        ))}
      </div>

      {/* Delete confirmation */}
      <ConfirmModal
        open={!!deleteTarget}
        onClose={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) {
            deleteMutation.mutate(deleteTarget, {
              onSuccess: () => {
                setDeleteTarget(null);
                setSelectedPath(null);
                setRevealedPath(null);
              },
            });
          }
        }}
        title="Delete Secret"
        message={`Permanently delete "${deleteTarget}"? This cannot be undone.`}
        confirmText={deleteTarget?.split('/').pop() ?? ''}
        confirmLabel="Delete Secret"
        destructive
        loading={deleteMutation.isPending}
      />
    </div>
  );
}
