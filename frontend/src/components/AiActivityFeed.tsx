import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Activity, Loader2 } from 'lucide-react';
import { ConfirmModal } from './ui/ConfirmModal';
import { aiActivityKey, callResult, useAiActivity, useRevokeActivityToken, type AiActivityCall } from '../api/aiActivity';

function errorText(err: unknown, fallback: string): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || fallback;
}

function TokenCell({ call, onRevoke }: { readonly call: AiActivityCall; readonly onRevoke: (call: AiActivityCall) => void }) {
  return (
    <td>
      <span>{call.tokenName || 'Unnamed token'}</span>
      {call.tokenId && (
        <button type="button" onClick={() => onRevoke(call)} className="ml-3 text-red-400 hover:text-red-300">Revoke</button>
      )}
      {call.tokenRevoked && <span className="ml-3 text-xs text-text-tertiary">Revoked</span>}
    </td>
  );
}

function revokeMessage(call: AiActivityCall | null): string {
  const whose = call && !call.mine ? ' This is another member\'s token.' : '';
  return `Revoke "${call?.tokenName ?? ''}"?${whose} The AI tool using it loses access immediately. This cannot be undone.`;
}

// What AI tools did in this project through MCP, newest first, with a way to
// cut off a token: the caller's own, or any member's for an org owner or admin.
export function AiActivityFeed({ projectId }: { readonly projectId: string }) {
  const activity = useAiActivity(projectId);
  const revokeToken = useRevokeActivityToken(projectId);
  const queryClient = useQueryClient();
  const [revoking, setRevoking] = useState<AiActivityCall | null>(null);
  const [error, setError] = useState<string | null>(null);

  const confirmRevoke = async () => {
    if (!revoking?.tokenId) return;
    setError(null);
    try {
      await revokeToken.mutateAsync(revoking.tokenId);
      await queryClient.invalidateQueries({ queryKey: aiActivityKey(projectId) });
    } catch (err) {
      setError(errorText(err, 'Could not revoke the token'));
    } finally {
      setRevoking(null);
    }
  };

  return (
    <section className="space-y-3" data-testid="ai-activity">
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary"><Activity className="w-4 h-4" /> AI activity</h4>
      {error && <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}
      {activity.isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-secondary" />}
      {activity.isError && <p className="text-sm text-red-400">{errorText(activity.error, 'Could not load the activity')}</p>}
      {activity.data?.length === 0 && <p className="text-sm text-text-secondary">No AI tool has called this project yet.</p>}
      {!!activity.data?.length && (
        <table className="w-full text-sm">
          <thead className="text-left text-text-secondary">
            <tr><th className="py-2">When</th><th>Tool</th><th>Token</th><th>Result</th></tr>
          </thead>
          <tbody>
            {activity.data.map((call) => (
              <tr key={call.id} data-testid={`ai-activity-${call.id}`} className="border-t border-border-primary text-text-primary">
                <td className="py-2">{new Date(call.at).toLocaleString()}</td>
                <td><code className="text-xs">{call.tool}</code></td>
                <TokenCell call={call} onRevoke={setRevoking} />
                <td className={call.status === 'ok' ? 'text-green-400' : 'text-red-400'}>{callResult(call)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <ConfirmModal
        open={revoking !== null}
        onClose={() => setRevoking(null)}
        onConfirm={() => { void confirmRevoke(); }}
        title="Revoke access token"
        message={revokeMessage(revoking)}
        confirmLabel="Revoke"
        destructive
        loading={revokeToken.isPending}
      />
    </section>
  );
}
