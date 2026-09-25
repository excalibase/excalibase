import { useState, useEffect } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { Settings, Users, Trash2, UserPlus, Shield, ChevronRight, Database, Clock } from 'lucide-react';
import { getOrg, listOrgMembers, inviteOrgMember, removeOrgMember, updateOrgMemberRole, updateOrg, deleteOrg, listPendingInvites, type Org, type OrgMember, type PendingInvite } from '../api/orgs';
import { api } from '../api/client';
import { Button } from '../components/Button';
import { InviteLinkNotice } from '../components/InviteLinkNotice';
import { useAuthStore } from '../stores/auth-store';

interface DatabaseInstance {
  projectId: string;
  orgId: string;
  status: string;
  tier: string;
}

const ROLE_COLORS: Record<string, string> = {
  owner: 'bg-yellow-500/20 text-yellow-400',
  admin: 'bg-purple-500/20 text-purple-400',
  developer: 'bg-blue-500/20 text-blue-400',
  viewer: 'bg-gray-500/20 text-gray-400',
};

export function OrgDetailPage() {
  const { orgId } = useParams<{ orgId: string }>();
  const navigate = useNavigate();
  const currentUser = useAuthStore((s) => s.user);

  const [org, setOrg] = useState<Org | null>(null);
  const [members, setMembers] = useState<OrgMember[]>([]);
  const [pendingInvites, setPendingInvites] = useState<PendingInvite[]>([]);
  const [projects, setProjects] = useState<DatabaseInstance[]>([]);
  const [loading, setLoading] = useState(true);
  const [tab, setTab] = useState<'projects' | 'members' | 'settings'>('projects');

  // Invite state
  const [showInvite, setShowInvite] = useState(false);
  const [inviteEmail, setInviteEmail] = useState('');
  const [inviteRole, setInviteRole] = useState('developer');
  const [inviteError, setInviteError] = useState<string | null>(null);
  const [issuedInvite, setIssuedInvite] = useState<{ email: string; link: string } | null>(null);

  const isOwnerOrAdmin = members.some(
    (m) => m.userId === currentUser?.id && (m.role === 'owner' || m.role === 'admin')
  );

  useEffect(() => {
    if (!orgId) return;
    const load = async () => {
      try {
        const [orgData, membersData, invitesData] = await Promise.all([
          getOrg(orgId),
          listOrgMembers(orgId),
          listPendingInvites(orgId).catch(() => [] as PendingInvite[]),
        ]);
        setOrg(orgData);
        setMembers(membersData);
        setPendingInvites(invitesData);

        // Load projects for this org
        try {
          const { data } = await api.get<DatabaseInstance[]>('/provision');
          setProjects(data.filter((p) => p.orgId === orgId));
        } catch {
          setProjects([]);
        }
      } finally {
        setLoading(false);
      }
    };
    load();
  }, [orgId]);

  const loadMembers = async () => {
    if (!orgId) return;
    const [data, invites] = await Promise.all([
      listOrgMembers(orgId),
      listPendingInvites(orgId).catch(() => [] as PendingInvite[]),
    ]);
    setMembers(data);
    setPendingInvites(invites);
  };

  const handleInvite = async () => {
    if (!orgId || !inviteEmail.trim()) return;
    setInviteError(null);
    try {
      const email = inviteEmail.trim();
      const result = await inviteOrgMember(orgId, email, inviteRole);
      setIssuedInvite(result.status === 'pending' && result.inviteLink ? { email, link: result.inviteLink } : null);
      setShowInvite(false);
      setInviteEmail('');
      loadMembers();
    } catch (err: unknown) {
      const axiosErr = err as { response?: { data?: { error?: string } } };
      setInviteError(axiosErr.response?.data?.error || 'Failed to invite member');
    }
  };

  const handleRemoveMember = async (userId: string) => {
    if (!orgId) return;
    await removeOrgMember(orgId, userId);
    loadMembers();
  };

  const handleRoleChange = async (userId: string, newRole: string) => {
    if (!orgId) return;
    await updateOrgMemberRole(orgId, userId, newRole);
    loadMembers();
  };

  const handleDeleteOrg = async () => {
    if (!orgId || !confirm('Delete this organization? This cannot be undone.')) return;
    await deleteOrg(orgId);
    navigate('/orgs');
  };


  if (loading || !org) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-purple-500" />
      </div>
    );
  }

  return (
    <div className="max-w-4xl mx-auto">
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold text-text-primary">{org.name}</h1>
          <p className="text-sm text-text-secondary mt-1">{org.slug} &middot; {org.tier}</p>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 mb-6 border-b border-border-primary">
        {(['projects', 'members', 'settings'] as const).map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
              tab === t
                ? 'border-purple-500 text-purple-400'
                : 'border-transparent text-text-secondary hover:text-text-primary'
            }`}
          >
            {t === 'projects' && <Database className="w-4 h-4 inline mr-1.5" />}
            {t === 'members' && <Users className="w-4 h-4 inline mr-1.5" />}
            {t === 'settings' && <Settings className="w-4 h-4 inline mr-1.5" />}
            {t.charAt(0).toUpperCase() + t.slice(1)}
          </button>
        ))}
      </div>

      {/* Projects tab */}
      {tab === 'projects' && (
        <div>
          {isOwnerOrAdmin && (
            <div className="mb-4">
              <Button onClick={() => navigate('/provision')} className="flex items-center gap-2">
                <Database className="w-4 h-4" /> New Project
              </Button>
            </div>
          )}
          {projects.length === 0 ? (
            <div className="text-center py-12 bg-surface-card border border-border-primary rounded-lg">
              <Database className="w-10 h-10 text-text-tertiary mx-auto mb-2" />
              <p className="text-text-secondary">No projects yet</p>
            </div>
          ) : (
            <div className="space-y-2">
              {projects.map((p) => (
                <button
                  key={p.projectId}
                  onClick={() => navigate(`/project/${p.projectId}`)}
                  className="w-full flex items-center gap-4 p-4 bg-surface-card border border-border-primary rounded-lg hover:border-purple-500/50 transition-colors text-left group"
                >
                  <Database className="w-5 h-5 text-green-400" />
                  <div className="flex-1">
                    <span className="font-medium text-text-primary">{p.projectId}</span>
                    <span className="ml-2 text-xs text-text-tertiary">{p.status}</span>
                  </div>
                  <ChevronRight className="w-5 h-5 text-text-tertiary group-hover:text-text-secondary" />
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Members tab */}
      {tab === 'members' && (
        <div>
          {isOwnerOrAdmin && (
            <div className="mb-4">
              <Button onClick={() => setShowInvite(!showInvite)} className="flex items-center gap-2">
                <UserPlus className="w-4 h-4" /> Invite Member
              </Button>
            </div>
          )}

          {showInvite && (
            <div className="mb-4 p-4 bg-surface-card border border-border-primary rounded-lg space-y-3">
              <h3 className="font-semibold text-text-primary text-sm">Invite Member by Email</h3>
              {inviteError && (
                <div className="px-3 py-2 rounded bg-red-500/10 border border-red-500/30 text-red-400 text-sm">
                  {inviteError}
                </div>
              )}
              <div className="flex gap-2">
                <input
                  type="email"
                  value={inviteEmail}
                  onChange={(e) => setInviteEmail(e.target.value)}
                  placeholder="user@example.com"
                  className="flex-1 px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm placeholder:text-text-tertiary"
                />
                <select
                  value={inviteRole}
                  onChange={(e) => setInviteRole(e.target.value)}
                  className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm"
                >
                  <option value="admin">Admin</option>
                  <option value="developer">Developer</option>
                  <option value="viewer">Viewer</option>
                </select>
                <Button onClick={handleInvite} disabled={!inviteEmail.trim()}>Invite</Button>
              </div>
              <p className="text-xs text-text-tertiary">An existing account is added at once; anyone else gets a one-time link to send them</p>
            </div>
          )}

          {issuedInvite && (
            <div className="mb-4">
              <InviteLinkNotice email={issuedInvite.email} link={issuedInvite.link} />
            </div>
          )}

          <div className="space-y-1">
            {members.map((m) => (
              <div key={m.userId} className="flex items-center gap-3 p-3 bg-surface-card border border-border-primary rounded-lg">
                <div className="w-8 h-8 rounded-full bg-purple-500/20 flex items-center justify-center text-sm font-medium text-purple-400">
                  {m.username.charAt(0).toUpperCase()}
                </div>
                <div className="flex-1 min-w-0">
                  <div className="font-medium text-text-primary text-sm">{m.username}</div>
                  <div className="text-xs text-text-tertiary">{m.email}</div>
                </div>
                <span className={`px-2 py-0.5 rounded text-xs font-medium ${ROLE_COLORS[m.role] || ROLE_COLORS.viewer}`}>
                  <Shield className="w-3 h-3 inline mr-1" />{m.role}
                </span>
                {isOwnerOrAdmin && m.role !== 'owner' && m.userId !== currentUser?.id && (
                  <div className="flex items-center gap-1">
                    <select
                      value={m.role}
                      onChange={(e) => handleRoleChange(m.userId, e.target.value)}
                      className="px-2 py-1 bg-bg-secondary border border-border-primary rounded text-xs text-text-primary"
                    >
                      <option value="admin">Admin</option>
                      <option value="developer">Developer</option>
                      <option value="viewer">Viewer</option>
                    </select>
                    <button
                      onClick={() => handleRemoveMember(m.userId)}
                      className="p-1 text-text-tertiary hover:text-red-400 transition-colors"
                      title="Remove member"
                    >
                      <Trash2 className="w-4 h-4" />
                    </button>
                  </div>
                )}
              </div>
            ))}
          </div>

          {/* Pending invites */}
          {pendingInvites.length > 0 && (
            <div className="mt-4">
              <h4 className="text-sm font-medium text-text-secondary mb-2 flex items-center gap-1">
                <Clock className="w-4 h-4" /> Pending Invites ({pendingInvites.length})
              </h4>
              <div className="space-y-1">
                {pendingInvites.map((inv) => (
                  <div key={inv.id} className="flex items-center gap-3 p-3 bg-surface-card border border-yellow-500/20 rounded-lg">
                    <div className="w-8 h-8 rounded-full bg-yellow-500/20 flex items-center justify-center text-sm font-medium text-yellow-400">
                      ?
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="font-medium text-text-primary text-sm">{inv.email}</div>
                      <div className="text-xs text-text-tertiary">Waiting for the invite link to be used</div>
                    </div>
                    <span className="px-2 py-0.5 rounded text-xs font-medium bg-yellow-500/20 text-yellow-400">
                      pending &middot; {inv.role}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      )}

      {/* Settings tab */}
      {tab === 'settings' && isOwnerOrAdmin && (
        <div className="space-y-6">
          <div className="p-4 bg-surface-card border border-border-primary rounded-lg space-y-3">
            <h3 className="font-semibold text-text-primary">Organization Settings</h3>
            <div>
              <label htmlFor="org-tier-select" className="block text-sm text-text-secondary mb-1">Tier</label>
              <select
                id="org-tier-select"
                value={org.tier}
                onChange={async (e) => {
                  const updated = await updateOrg(org.id, { tier: e.target.value });
                  setOrg(updated);
                }}
                className="px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm"
              >
                <option value="FREE">Free</option>
                <option value="STANDARD">Standard</option>
                <option value="ENTERPRISE">Enterprise</option>
              </select>
            </div>
          </div>

          <div className="p-4 bg-red-500/5 border border-red-500/20 rounded-lg">
            <h3 className="font-semibold text-red-400 mb-2">Danger Zone</h3>
            <p className="text-sm text-text-secondary mb-3">
              Deleting this organization will remove all associated data. This action cannot be undone.
            </p>
            <Button onClick={handleDeleteOrg} className="bg-red-500 hover:bg-red-600 text-white">
              <Trash2 className="w-4 h-4 inline mr-1" /> Delete Organization
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
