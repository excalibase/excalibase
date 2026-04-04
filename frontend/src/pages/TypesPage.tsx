import { useParams } from 'react-router-dom';
import { Loader2, Hash } from 'lucide-react';
import { usePgTypes } from '../hooks/useSchema';

export function TypesPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: types = [], isLoading } = usePgTypes(projectId || '');

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="types-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">User-Defined Types</h3>
      </div>

      {types.length === 0 ? (
        <div className="rounded-lg border border-border-primary bg-surface-card p-12 text-center text-text-tertiary text-sm">
          No user-defined types found
        </div>
      ) : (
        <div className="rounded-lg border border-border-primary bg-surface-card overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-surface-card">
                <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Name</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Schema</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Category</th>
                <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Values</th>
              </tr>
            </thead>
            <tbody>
              {types.map((t) => (
                <tr key={t.name} className="border-t border-border-primary hover:bg-surface-hover">
                  <td className="px-4 py-3 text-text-primary font-medium flex items-center gap-2">
                    <Hash className="w-4 h-4 text-purple-400" />
                    {t.name}
                  </td>
                  <td className="px-4 py-3 text-text-secondary">{t.schema}</td>
                  <td className="px-4 py-3">
                    <span className="inline-flex items-center px-2 py-0.5 rounded text-xs bg-purple-900/20 text-purple-400 border border-purple-500/30">
                      {t.type}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    {t.type === 'enum' && t.values.length > 0 ? (
                      <div className="flex flex-wrap gap-1">
                        {t.values.map((v) => (
                          <span key={v} className="inline-flex items-center px-2 py-0.5 rounded text-xs bg-blue-900/20 text-blue-400 border border-blue-500/30">
                            {v}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="text-text-tertiary text-xs">-</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
