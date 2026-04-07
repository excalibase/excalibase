import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useProvisionDatabase } from '../hooks/useProvisioning';
import { DatabaseType, TierType } from '../types';
import { Button } from '../components/Button';
import { Database, Loader2 } from 'lucide-react';
import { listMyOrgs, type Org } from '../api/orgs';

const DB_TYPES = [
  { type: DatabaseType.POSTGRESQL, icon: '🐘', label: 'PostgreSQL', desc: 'CloudNativePG operator', disabled: false },
  { type: DatabaseType.MYSQL,      icon: '🐬', label: 'MySQL',      desc: 'Coming soon',            disabled: true  },
  { type: DatabaseType.MONGODB,    icon: '🍃', label: 'MongoDB',    desc: 'Coming soon',            disabled: true  },
];

const TIERS = [
  { tier: TierType.FREE,       label: 'Free',       features: ['1 instance', '5 GB storage', '512 MB RAM', '0.5 vCPU'] },
  { tier: TierType.STANDARD,   label: 'Standard',   features: ['3 replicas', '50 GB storage', '4 GB RAM', '2 vCPU'] },
  { tier: TierType.ENTERPRISE, label: 'Enterprise', features: ['5 replicas', '500 GB storage', '16 GB RAM', '4 vCPU'] },
];

export function ProvisionPage() {
  const navigate = useNavigate();
  const provision = useProvisionDatabase();

  const [orgs, setOrgs] = useState<Org[]>([]);
  const [projectName, setProjectName] = useState('');
  const [orgId, setOrgId] = useState('');
  const [dbType, setDbType] = useState<DatabaseType>(DatabaseType.POSTGRESQL);
  const [tier, setTier] = useState<TierType>(TierType.FREE);

  useEffect(() => {
    listMyOrgs().then((data) => {
      setOrgs(data);
      if (data.length === 1) {
        setOrgId(data[0].id);
      }
    });
  }, []);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await provision.mutateAsync({ projectName, orgId, databaseType: dbType, tier });
    navigate(`/project/${result.projectId}`);
  };

  return (
    <div className="max-w-3xl mx-auto space-y-8">
      <form onSubmit={handleSubmit} className="space-y-8">
        <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-5">
          <h2 className="font-semibold text-text-primary">Instance Details</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label className="block text-sm font-medium text-text-primary mb-1.5">Project Name</label>
              <input
                type="text"
                value={projectName}
                onChange={(e) => setProjectName(e.target.value)}
                placeholder="my-database"
                required
                className="w-full px-4 py-2.5 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              />
              <p className="text-xs text-text-tertiary mt-1">Lowercase, alphanumeric and hyphens only</p>
            </div>
            <div>
              <label className="block text-sm font-medium text-text-primary mb-1.5">Organization</label>
              <select
                value={orgId}
                onChange={(e) => setOrgId(e.target.value)}
                required
                className="w-full px-4 py-2.5 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              >
                <option value="">Select organization...</option>
                {orgs.map((org) => (
                  <option key={org.id} value={org.id}>{org.name} ({org.tier})</option>
                ))}
              </select>
              {orgs.length === 0 && (
                <p className="text-xs text-red-400 mt-1">
                  No organizations found. <a href="/orgs" className="underline">Create one first</a>.
                </p>
              )}
            </div>
          </div>
        </div>

        <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
          <h2 className="font-semibold text-text-primary">Database Engine</h2>
          <div className="grid grid-cols-3 gap-3">
            {DB_TYPES.map(({ type, icon, label, desc, disabled }) => (
              <button
                key={type}
                type="button"
                onClick={() => !disabled && setDbType(type)}
                disabled={disabled}
                className={`p-4 rounded-xl border-2 text-left transition-all ${
                  disabled
                    ? 'border-border-primary bg-bg-secondary opacity-50 cursor-not-allowed'
                    : dbType === type
                    ? 'border-accent-primary bg-accent-primary/10'
                    : 'border-border-primary bg-bg-tertiary hover:border-border-secondary'
                }`}
              >
                <div className="text-3xl mb-2">{icon}</div>
                <p className="font-semibold text-text-primary text-sm">{label}</p>
                <p className="text-xs text-text-tertiary mt-0.5">{desc}</p>
              </button>
            ))}
          </div>
        </div>

        <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
          <h2 className="font-semibold text-text-primary">Plan</h2>
          <div className="grid grid-cols-3 gap-3">
            {TIERS.map(({ tier: t, label, features }) => (
              <button
                key={t}
                type="button"
                onClick={() => setTier(t)}
                className={`p-4 rounded-xl border-2 text-left transition-all ${
                  tier === t
                    ? 'border-accent-primary bg-accent-primary/10'
                    : 'border-border-primary bg-bg-tertiary hover:border-border-secondary'
                }`}
              >
                <p className="font-semibold text-text-primary mb-2">{label}</p>
                <ul className="space-y-1">
                  {features.map((f) => (
                    <li key={f} className="text-xs text-text-secondary">· {f}</li>
                  ))}
                </ul>
              </button>
            ))}
          </div>
        </div>

        {provision.isError && (
          <div className="bg-red-900/20 border border-color-error rounded-lg p-4">
            <p className="text-color-error text-sm">{(provision.error as Error).message}</p>
          </div>
        )}
        <div className="flex gap-3">
          <Button type="button" variant="secondary" className="flex-1" onClick={() => navigate('/instances')}>
            Cancel
          </Button>
          <Button type="submit" className="flex-1" disabled={provision.isPending || !orgId}>
            {provision.isPending
              ? <><Loader2 className="w-4 h-4 mr-2 animate-spin inline" /> Provisioning...</>
              : <><Database className="w-4 h-4 mr-2 inline" /> Provision Database</>
            }
          </Button>
        </div>
      </form>
    </div>
  );
}
