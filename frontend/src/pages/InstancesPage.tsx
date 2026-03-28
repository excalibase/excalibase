import { useNavigate } from 'react-router-dom';
import { useInstances, useDeprovisionDatabase } from '../hooks/useProvisioning';
import { StatusBadge } from '../components/shared/StatusBadge';
import { Button } from '../components/Button';
import { Database, DatabaseZap, Loader2, Trash2, Eye, Sprout, Star, Crown } from 'lucide-react';

export function InstancesPage() {
  const { data: instances = [], isLoading } = useInstances();
  const deprovision = useDeprovisionDatabase();
  const navigate = useNavigate();

  const handleDelete = async (e: React.MouseEvent, projectId: string) => {
    e.stopPropagation();
    if (confirm(`Delete ${projectId}? This cannot be undone.`)) {
      await deprovision.mutateAsync(projectId);
    }
  };

  return (
    <div className="max-w-7xl mx-auto space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-text-secondary text-sm">{instances.length} instance{instances.length !== 1 ? 's' : ''}</p>
        <Button size="sm" onClick={() => navigate('/provision')}>
          <DatabaseZap className="w-4 h-4 mr-1.5" />
          Provision New
        </Button>
      </div>

      <div className="bg-surface-card border border-border-primary rounded-xl overflow-hidden">
        {isLoading ? (
          <div className="flex items-center justify-center py-16">
            <Loader2 className="w-6 h-6 animate-spin text-accent-primary" />
          </div>
        ) : instances.length === 0 ? (
          <div className="text-center py-16">
            <Database className="w-12 h-12 mx-auto mb-3 text-text-tertiary" />
            <p className="text-text-secondary mb-4">No database instances found.</p>
            <Button onClick={() => navigate('/provision')}>Create your first database</Button>
          </div>
        ) : (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary bg-bg-secondary">
                <th className="text-left px-6 py-3 font-medium">Project</th>
                <th className="text-left px-6 py-3 font-medium">Type</th>
                <th className="text-left px-6 py-3 font-medium">Tier</th>
                <th className="text-left px-6 py-3 font-medium">Namespace</th>
                <th className="text-left px-6 py-3 font-medium">Status</th>
                <th className="text-left px-6 py-3 font-medium">Created</th>
                <th className="px-6 py-3" />
              </tr>
            </thead>
            <tbody>
              {instances.map((inst) => (
                <tr
                  key={inst.projectId}
                  className="border-b border-border-primary last:border-0 hover:bg-surface-hover transition-colors cursor-pointer"
                  onClick={() => navigate(`/instances/${inst.projectId}`)}
                >
                  <td className="px-6 py-4">
                    <div className="flex items-center gap-2">
                      <span className="text-lg">{dbIcon(inst.databaseType)}</span>
                      <span className="font-medium text-text-primary">{inst.projectId}</span>
                    </div>
                  </td>
                  <td className="px-6 py-4 text-text-secondary">{inst.databaseType}</td>
                  <td className="px-6 py-4">
                    <TierBadge tier={inst.tier} />
                  </td>
                  <td className="px-6 py-4 text-text-tertiary font-mono text-xs">{inst.namespace}</td>
                  <td className="px-6 py-4"><StatusBadge stage={inst.currentStage} /></td>
                  <td className="px-6 py-4 text-text-tertiary">{new Date(inst.createdAt).toLocaleDateString()}</td>
                  <td className="px-6 py-4">
                    <div className="flex items-center gap-2 justify-end" onClick={(e) => e.stopPropagation()}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={() => navigate(`/instances/${inst.projectId}`)}
                      >
                        <Eye className="w-4 h-4" />
                      </Button>
                      <Button
                        variant="danger"
                        size="sm"
                        disabled={deprovision.isPending}
                        onClick={(e) => handleDelete(e, inst.projectId)}
                      >
                        <Trash2 className="w-4 h-4" />
                      </Button>
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

function dbIcon(type: string) {
  if (type === 'POSTGRESQL') return '🐘';
  if (type === 'MYSQL')      return '🐬';
  if (type === 'MONGODB')    return '🍃';
  return '🗄️';
}

const TIER_CONFIG: Record<string, { style: string; icon: React.ElementType; label: string }> = {
  FREE:       { style: 'bg-bg-tertiary text-text-secondary border-border-secondary',          icon: Sprout, label: 'Free'       },
  STANDARD:   { style: 'bg-blue-500/10 text-blue-400 border-blue-500/30',                    icon: Star,   label: 'Standard'  },
  ENTERPRISE: { style: 'bg-violet-500/10 text-violet-400 border-violet-500/30',              icon: Crown,  label: 'Enterprise'},
};

function TierBadge({ tier }: { tier: string }) {
  const cfg = TIER_CONFIG[tier] ?? TIER_CONFIG.FREE;
  const Icon = cfg.icon;
  return (
    <span className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs border font-medium ${cfg.style}`}>
      <Icon className="w-3 h-3" />{cfg.label}
    </span>
  );
}
