import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { useProvisionDatabase } from '../hooks/useProvisioning';
import { DatabaseType, TierType } from '../types';
import { Button } from '../components/Button';
import { Database, Loader2, Server, Cloud } from 'lucide-react';
import { listMyOrgs, type Org } from '../api/orgs';
import { useTiers, sortTiers, type TierConfig } from '../api/tiers';
import { usePostgresCatalog, findMajor, type PostgresMajor } from '../api/postgresCatalog';
import { PostgresVersionPicker } from '../components/PostgresVersionPicker';

type DeployMode = 'k8s' | 'docker';

function optionTileClass(disabled: boolean | undefined, selected: boolean): string {
  if (disabled) return 'border-border-primary bg-bg-secondary opacity-50 cursor-not-allowed';
  if (selected) return 'border-accent-primary bg-accent-primary/10';
  return 'border-border-primary bg-bg-tertiary hover:border-border-secondary';
}

const DEPLOY_MODES = [
  { mode: 'k8s' as DeployMode, icon: Cloud, label: 'Kubernetes', desc: 'CloudNativePG operator' },
  { mode: 'docker' as DeployMode, icon: Server, label: 'Docker', desc: 'Docker container (coming soon)', disabled: true },
];

const DB_TYPES = [
  { type: DatabaseType.POSTGRESQL, icon: '🐘', label: 'PostgreSQL', desc: 'CloudNativePG operator', disabled: false },
  { type: DatabaseType.MYSQL,      icon: '🐬', label: 'MySQL',      desc: 'Coming soon',            disabled: true  },
  { type: DatabaseType.MONGODB,    icon: '🍃', label: 'MongoDB',    desc: 'Coming soon',            disabled: true  },
];

// Shown only while the live tier specs load (or if the endpoint is unreachable).
// Tiers are single-instance on the current alpha; the real specs come from
// GET /api/tiers and are admin-editable.
const FALLBACK_TIERS = [
  { tier: TierType.FREE,       label: 'Free',       features: ['1 instance', '5Gi storage', '512Mi RAM', '0.5 vCPU'] },
  { tier: TierType.STANDARD,   label: 'Standard',   features: ['1 instance', '50Gi storage', '4Gi RAM', '2 vCPU'] },
  { tier: TierType.ENTERPRISE, label: 'Enterprise', features: ['1 instance', '500Gi storage', '16Gi RAM', '4 vCPU'] },
];

function tierLabel(tier: string): string {
  return tier.charAt(0) + tier.slice(1).toLowerCase();
}

function tierFeatures(tc: TierConfig): string[] {
  return [
    tc.instances === 1 ? '1 instance' : `${tc.instances} instances`,
    `${tc.storageSize} storage`,
    `${tc.memory} RAM`,
    `${tc.cpu} vCPU`,
    ...(tc.backupEnabled ? ['Backups'] : []),
  ];
}

export function ProvisionPage() {
  const navigate = useNavigate();
  const provision = useProvisionDatabase();

  const [orgs, setOrgs] = useState<Org[]>([]);
  const [deployMode, setDeployMode] = useState<DeployMode>('k8s');
  const [projectName, setProjectName] = useState('');
  const [orgId, setOrgId] = useState('');
  const [dbType, setDbType] = useState<DatabaseType>(DatabaseType.POSTGRESQL);
  const [tier, setTier] = useState<TierType>(TierType.FREE);
  // No default major, on purpose: see PostgresVersionPicker.
  const [postgresVersion, setPostgresVersion] = useState('');
  const [documentDb, setDocumentDb] = useState(false);

  const catalog = usePostgresCatalog();

  // A major that cannot carry DocumentDB clears the choice instead of leaving
  // it set and invisible, so what the form shows is what it will send.
  const chooseVersion = (version: string) => {
    setPostgresVersion(version);
    const entry: PostgresMajor | undefined = findMajor(catalog.data, version);
    if (!entry?.documentDb) setDocumentDb(false);
  };

  // Live tier specs (admin-editable, DB-backed). Falls back to a static list
  // while loading or if the endpoint is unreachable.
  const { data: tierConfigs } = useTiers();
  const tierOptions = tierConfigs && tierConfigs.length > 0
    ? sortTiers(tierConfigs).map((tc) => ({ tier: tc.tier, label: tierLabel(tc.tier), features: tierFeatures(tc) }))
    : FALLBACK_TIERS;

  useEffect(() => {
    listMyOrgs().then((data) => {
      setOrgs(data);
      if (data.length === 1) setOrgId(data[0].id);
    });
  }, []);

  const isPending = provision.isPending;
  const error = provision.error;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const result = await provision.mutateAsync({
      projectName,
      orgId,
      databaseType: dbType,
      tier,
      postgresVersion,
      documentDb,
    });
    navigate(`/project/${result.projectId}`);
  };

  return (
    <div className="max-w-3xl mx-auto space-y-8" data-testid="provision-page">
      <form onSubmit={handleSubmit} className="space-y-8">
        {/* Deployment Mode */}
        <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
          <h2 className="font-semibold text-text-primary">Deployment Mode</h2>
          <div className="grid grid-cols-2 gap-3" data-testid="deploy-mode-selector">
            {DEPLOY_MODES.map(({ mode, icon: Icon, label, desc, disabled }) => {
              const modeClass = optionTileClass(disabled, deployMode === mode);
              return (
              <button
                key={mode}
                type="button"
                onClick={() => !disabled && setDeployMode(mode)}
                disabled={disabled}
                data-testid={`deploy-mode-${mode}`}
                className={`p-4 rounded-xl border-2 text-left transition-all ${modeClass}`}
              >
                <Icon className="w-8 h-8 mb-2 text-text-primary" />
                <p className="font-semibold text-text-primary text-sm">{label}</p>
                <p className="text-xs text-text-tertiary mt-0.5">{desc}</p>
              </button>
              );
            })}
          </div>
        </div>

        {/* Instance Details */}
        <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-5">
          <h2 className="font-semibold text-text-primary">Instance Details</h2>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
              <label htmlFor="provision-project-name" className="block text-sm font-medium text-text-primary mb-1.5">Project Name</label>
              <input
                id="provision-project-name"
                type="text" value={projectName} onChange={(e) => setProjectName(e.target.value)}
                placeholder="my-database" required
                className="w-full px-4 py-2.5 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary placeholder:text-text-tertiary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              />
            </div>
            <div>
              <label htmlFor="provision-org-select" className="block text-sm font-medium text-text-primary mb-1.5">Organization</label>
              <select
                id="provision-org-select"
                value={orgId} onChange={(e) => setOrgId(e.target.value)} required
                className="w-full px-4 py-2.5 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              >
                <option value="">Select organization...</option>
                {orgs.map((org) => (
                  <option key={org.id} value={org.id}>{org.name} ({org.tier})</option>
                ))}
              </select>
            </div>
          </div>
        </div>

        {/* K8s-specific: DB Engine + Tier */}
        {deployMode === 'k8s' && (
          <>
            <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
              <h2 className="font-semibold text-text-primary">Database Engine</h2>
              <div className="grid grid-cols-3 gap-3">
                {DB_TYPES.map(({ type, icon, label, desc, disabled }) => (
                  <button key={type} type="button" onClick={() => !disabled && setDbType(type)} disabled={disabled}
                    className={`p-4 rounded-xl border-2 text-left transition-all ${optionTileClass(disabled, dbType === type)}`}
                  >
                    <div className="text-3xl mb-2">{icon}</div>
                    <p className="font-semibold text-text-primary text-sm">{label}</p>
                    <p className="text-xs text-text-tertiary mt-0.5">{desc}</p>
                  </button>
                ))}
              </div>
            </div>

            <PostgresVersionPicker
              catalog={catalog.data}
              isLoading={catalog.isLoading}
              error={catalog.error}
              version={postgresVersion}
              onVersionChange={chooseVersion}
              documentDb={documentDb}
              onDocumentDbChange={setDocumentDb}
            />

            <div className="bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
              <h2 className="font-semibold text-text-primary">Plan</h2>
              <div className="grid grid-cols-3 gap-3">
                {tierOptions.map(({ tier: t, label, features }) => (
                  <button key={t} type="button" onClick={() => setTier(t)}
                    className={`p-4 rounded-xl border-2 text-left transition-all ${
                      tier === t ? 'border-accent-primary bg-accent-primary/10'
                        : 'border-border-primary bg-bg-tertiary hover:border-border-secondary'
                    }`}
                  >
                    <p className="font-semibold text-text-primary mb-2">{label}</p>
                    <ul className="space-y-1">
                      {features.map((f) => (<li key={f} className="text-xs text-text-secondary">{'\u00b7'} {f}</li>))}
                    </ul>
                  </button>
                ))}
              </div>
            </div>
          </>
        )}

        {error && (
          <div className="bg-red-900/20 border border-color-error rounded-lg p-4">
            <p className="text-color-error text-sm">{error instanceof Error ? error.message : String(error)}</p>
          </div>
        )}
        <div className="flex gap-3">
          <Button type="button" variant="secondary" className="flex-1" onClick={() => navigate('/instances')}>
            Cancel
          </Button>
          <Button
            type="submit"
            className="flex-1"
            data-testid="provision-submit"
            disabled={isPending || !orgId || !postgresVersion}
          >
            {isPending
              ? <><Loader2 className="w-4 h-4 mr-2 animate-spin inline" /> Provisioning...</>
              : <><Database className="w-4 h-4 mr-2 inline" /> Provision Database</>}
          </Button>
        </div>
      </form>
    </div>
  );
}
