import { useParams } from 'react-router-dom';
import { Loader2, KeyRound, Clock, AlertCircle } from 'lucide-react';
import { useAuthSessions } from '../hooks/useAuthUsers';

export function AuthSessionsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const { data: sessions = [], isLoading } = useAuthSessions(projectId || '');

  const active = sessions.filter(s => !s.revoked && new Date(s.expiry_date) > new Date());
  const expired = sessions.filter(s => s.revoked || new Date(s.expiry_date) <= new Date());

  if (isLoading) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  return (
    <div data-testid="auth-sessions-page">
      <h3 className="text-lg font-semibold text-text-primary mb-1">Sessions</h3>
      <p className="text-sm text-text-secondary mb-6">Active refresh tokens for {projectId}</p>

      {/* Active sessions */}
      <div className="mb-6">
        <h4 className="text-sm font-medium text-text-secondary mb-2">Active ({active.length})</h4>
        <div className="rounded-lg border border-border-primary overflow-hidden">
          <table className="w-full text-sm">
            <thead>
              <tr className="bg-surface-card">
                <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">User</th>
                <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Created</th>
                <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Expires</th>
              </tr>
            </thead>
            <tbody>
              {active.map(s => (
                <tr key={s.id} className="border-t border-border-primary hover:bg-surface-hover">
                  <td className="px-4 py-2 text-text-primary flex items-center gap-2">
                    <KeyRound className="w-3.5 h-3.5 text-green-400" />
                    {s.email}
                  </td>
                  <td className="px-4 py-2 text-text-tertiary text-xs">{new Date(s.created_at).toLocaleString()}</td>
                  <td className="px-4 py-2 text-text-tertiary text-xs flex items-center gap-1">
                    <Clock className="w-3 h-3" />
                    {new Date(s.expiry_date).toLocaleString()}
                  </td>
                </tr>
              ))}
              {active.length === 0 && (
                <tr><td colSpan={3} className="px-4 py-6 text-center text-text-tertiary text-sm">No active sessions</td></tr>
              )}
            </tbody>
          </table>
        </div>
      </div>

      {/* Expired/revoked */}
      {expired.length > 0 && (
        <div>
          <h4 className="text-sm font-medium text-text-secondary mb-2">Expired / Revoked ({expired.length})</h4>
          <div className="rounded-lg border border-border-primary overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="bg-surface-card">
                  <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">User</th>
                  <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Status</th>
                  <th className="px-4 py-2 text-left text-xs font-medium text-text-secondary">Expired</th>
                </tr>
              </thead>
              <tbody>
                {expired.map(s => (
                  <tr key={s.id} className="border-t border-border-primary">
                    <td className="px-4 py-2 text-text-tertiary flex items-center gap-2">
                      <AlertCircle className="w-3.5 h-3.5 text-text-tertiary" />
                      {s.email}
                    </td>
                    <td className="px-4 py-2 text-xs">{s.revoked ? <span className="text-red-400">Revoked</span> : <span className="text-text-tertiary">Expired</span>}</td>
                    <td className="px-4 py-2 text-text-tertiary text-xs">{new Date(s.expiry_date).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
    </div>
  );
}
