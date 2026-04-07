import { useState, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { Building2, Plus, Crown, Users, ChevronRight } from 'lucide-react';
import { listMyOrgs, createOrg, type Org } from '../api/orgs';
import { Button } from '../components/Button';

const TIER_COLORS: Record<string, string> = {
  FREE: 'bg-gray-500/20 text-gray-400',
  STANDARD: 'bg-blue-500/20 text-blue-400',
  ENTERPRISE: 'bg-purple-500/20 text-purple-400',
};

export function OrgsPage() {
  const navigate = useNavigate();
  const [orgs, setOrgs] = useState<Org[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState('');
  const [newSlug, setNewSlug] = useState('');
  const [createError, setCreateError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);

  const fetchOrgs = async () => {
    try {
      const data = await listMyOrgs();
      setOrgs(data);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { fetchOrgs(); }, []);

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setCreateError(null);
    setCreating(true);
    try {
      const org = await createOrg(newName.trim(), newSlug.trim());
      setOrgs((prev) => [...prev, org]);
      setShowCreate(false);
      setNewName('');
      setNewSlug('');
      navigate(`/orgs/${org.id}`);
    } catch (err: unknown) {
      const axiosErr = err as { response?: { data?: { error?: string } } };
      setCreateError(axiosErr.response?.data?.error || 'Failed to create organization');
    } finally {
      setCreating(false);
    }
  };

  const handleNameChange = (name: string) => {
    setNewName(name);
    setNewSlug(name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, ''));
  };

  if (loading) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-purple-500" />
      </div>
    );
  }

  return (
    <div className="max-w-3xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-text-primary">Organizations</h1>
          <p className="text-sm text-text-secondary mt-1">Manage your organizations and projects</p>
        </div>
        <Button onClick={() => setShowCreate(true)} className="flex items-center gap-2">
          <Plus className="w-4 h-4" /> New Organization
        </Button>
      </div>

      {showCreate && (
        <form onSubmit={handleCreate} className="mb-6 p-4 bg-surface-card border border-border-primary rounded-lg space-y-3">
          <h3 className="font-semibold text-text-primary">Create Organization</h3>
          {createError && (
            <div className="px-3 py-2 rounded bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
              {createError}
            </div>
          )}
          <div>
            <label className="block text-sm text-text-secondary mb-1">Name</label>
            <input
              value={newName}
              onChange={(e) => handleNameChange(e.target.value)}
              className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="My Company"
              autoFocus
            />
          </div>
          <div>
            <label className="block text-sm text-text-secondary mb-1">Slug</label>
            <input
              value={newSlug}
              onChange={(e) => setNewSlug(e.target.value)}
              className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
              placeholder="my-company"
            />
            <p className="text-xs text-text-tertiary mt-1">Lowercase letters, numbers, and hyphens only</p>
          </div>
          <div className="flex gap-2">
            <Button type="submit" disabled={creating || !newName.trim() || !newSlug.trim()}>
              {creating ? 'Creating...' : 'Create'}
            </Button>
            <Button type="button" onClick={() => setShowCreate(false)} className="bg-transparent border border-border-primary text-text-secondary hover:bg-surface-hover">
              Cancel
            </Button>
          </div>
        </form>
      )}

      {orgs.length === 0 && !showCreate ? (
        <div className="text-center py-16 bg-surface-card border border-border-primary rounded-lg">
          <Building2 className="w-12 h-12 text-text-tertiary mx-auto mb-3" />
          <h3 className="text-lg font-semibold text-text-primary mb-1">No organizations yet</h3>
          <p className="text-sm text-text-secondary mb-4">Create your first organization to get started</p>
          <Button onClick={() => setShowCreate(true)} className="inline-flex items-center gap-2">
            <Plus className="w-4 h-4" /> Create Organization
          </Button>
        </div>
      ) : (
        <div className="space-y-2">
          {orgs.map((org) => (
            <button
              key={org.id}
              onClick={() => navigate(`/orgs/${org.id}`)}
              className="w-full flex items-center gap-4 p-4 bg-surface-card border border-border-primary rounded-lg hover:border-purple-500/50 transition-colors text-left group"
            >
              <div className="w-10 h-10 rounded-lg bg-purple-500/20 flex items-center justify-center flex-shrink-0">
                <Building2 className="w-5 h-5 text-purple-400" />
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span className="font-semibold text-text-primary">{org.name}</span>
                  <span className={`px-2 py-0.5 rounded text-xs font-medium ${TIER_COLORS[org.tier] || TIER_COLORS.FREE}`}>
                    {org.tier}
                  </span>
                </div>
                <div className="flex items-center gap-3 mt-0.5">
                  <span className="text-xs text-text-tertiary flex items-center gap-1">
                    <Crown className="w-3 h-3" /> {org.slug}
                  </span>
                </div>
              </div>
              <ChevronRight className="w-5 h-5 text-text-tertiary group-hover:text-text-secondary transition-colors" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
