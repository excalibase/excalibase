import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { Loader2 } from 'lucide-react';
import {
  deleteEndUser,
  endUserName,
  getEndUser,
  updateEndUser,
  type EndUser,
  type EndUserDetail,
} from '../../api/endUsers';
import { apiErrorMessage } from '../../api/permissions';
import { ConfirmModal } from '../ui/ConfirmModal';
import { useFeatureEnabled } from '../../hooks/useDeploymentMode';
import { ResetEndUserMFA } from './ResetEndUserMFA';
import { metadataMergePatch, parseMetadataObject } from './metadataPatch';

// One end user's details (EXC-662, flag `authuser`): user metadata is the
// user's own and read-only here; app metadata is the admin's to edit.

interface Props {
  readonly projectId: string;
  readonly user: EndUser;
  readonly onDeleted: () => void;
}

const endUserKey = (projectId: string, userId: number) => ['end-user', projectId, userId] as const;

export function EndUserDetails({ projectId, user, onDeleted }: Props) {
  const { enabled: mfa } = useFeatureEnabled('authmfa');
  const detail = useQuery({
    queryKey: endUserKey(projectId, user.id),
    queryFn: () => getEndUser(projectId, user.id),
  });

  if (detail.isLoading) return <Loader2 className="w-5 h-5 animate-spin text-purple-400" />;
  if (detail.error || !detail.data) {
    return (
      <p role="alert" className="text-sm text-red-400">
        {apiErrorMessage(detail.error, 'The end user could not be loaded.')}
      </p>
    );
  }
  return (
    <div className="space-y-4">
      <dl className="grid grid-cols-[8rem_1fr] gap-y-1 text-sm">
        <dt className="text-text-tertiary">Full name</dt>
        <dd className="text-text-primary">{detail.data.fullName || '—'}</dd>
        <dt className="text-text-tertiary">Email</dt>
        <dd className="text-text-primary">{detail.data.email || '—'}</dd>
      </dl>
      <div>
        <h4 className="text-xs font-medium text-text-secondary mb-1">
          User metadata (set by the user)
        </h4>
        <pre
          data-testid="user-metadata"
          className="p-2 rounded border border-border-primary bg-bg-primary text-xs font-mono overflow-auto"
        >
          {JSON.stringify(detail.data.userMetadata ?? {}, null, 2)}
        </pre>
      </div>
      {/* Keyed on the stored value so a save or refetch resets the box. */}
      <AppMetadataEditor
        key={JSON.stringify(detail.data.appMetadata ?? {})}
        projectId={projectId}
        detail={detail.data}
      />
      {mfa && (
        <div className="pt-2 border-t border-border-primary">
          <ResetEndUserMFA projectId={projectId} user={user} />
        </div>
      )}
      <DeleteEndUser projectId={projectId} user={user} onDeleted={onDeleted} />
    </div>
  );
}

function AppMetadataEditor({
  projectId,
  detail,
}: Readonly<{ projectId: string; detail: EndUserDetail }>) {
  const qc = useQueryClient();
  const saved = detail.appMetadata ?? {};
  const [text, setText] = useState(() => JSON.stringify(saved, null, 2));
  const [error, setError] = useState<string | null>(null);

  const save = useMutation({
    mutationFn: (appMetadata: Record<string, unknown>) =>
      updateEndUser(projectId, detail.id, { appMetadata }),
    onSuccess: (updated) => {
      toast.success(`App metadata of ${endUserName(detail)} saved`);
      qc.setQueryData(endUserKey(projectId, detail.id), updated);
    },
    onError: (err) => setError(apiErrorMessage(err, 'The app metadata could not be saved.')),
  });

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const parsed = parseMetadataObject(text);
    if (!parsed.value) {
      setError(parsed.error ?? 'Metadata must be a JSON object.');
      return;
    }
    const patch = metadataMergePatch(saved, parsed.value);
    if (Object.keys(patch).length === 0) {
      setError('Nothing changed.');
      return;
    }
    setError(null);
    save.mutate(patch);
  };

  return (
    <form onSubmit={submit} className="space-y-2">
      <label
        className="block text-xs font-medium text-text-secondary"
        htmlFor={`app-metadata-${detail.id}`}
      >
        App metadata (only admins and service keys can write it)
      </label>
      <textarea
        id={`app-metadata-${detail.id}`}
        aria-label={`App metadata of ${endUserName(detail)}`}
        value={text}
        onChange={(e) => setText(e.target.value)}
        rows={6}
        spellCheck={false}
        className="w-full p-2 rounded border border-border-primary bg-bg-primary text-text-primary text-xs font-mono"
      />
      {error && (
        <p role="alert" className="text-xs text-red-400">
          {error}
        </p>
      )}
      <div className="flex justify-end">
        <button
          type="submit"
          disabled={save.isPending}
          className="px-3 py-1 text-xs rounded bg-purple-500 hover:bg-purple-600 text-white disabled:opacity-50"
        >
          {save.isPending ? 'Saving...' : 'Save app metadata'}
        </button>
      </div>
    </form>
  );
}

function DeleteEndUser({ projectId, user, onDeleted }: Props) {
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const remove = useMutation({
    mutationFn: () => deleteEndUser(projectId, user.id),
    onSuccess: () => {
      toast.success(`${endUserName(user)} was deleted`);
      qc.invalidateQueries({ queryKey: ['end-users', projectId] });
      onDeleted();
    },
    onError: (err) => setError(apiErrorMessage(err, 'The end user could not be deleted.')),
  });

  return (
    <div className="pt-2 border-t border-border-primary">
      {error && (
        <p role="alert" className="text-xs text-red-400 mb-2">
          {error}
        </p>
      )}
      <button
        type="button"
        onClick={() => setConfirming(true)}
        className="px-3 py-1 text-xs rounded text-red-400 hover:bg-red-500/10"
      >
        Delete user
      </button>
      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          setError(null);
          return remove.mutateAsync().catch(() => undefined);
        }}
        title="Delete this end user?"
        message={`${endUserName(user)} loses their account and every session. This cannot be undone.`}
        confirmLabel="Delete permanently"
        confirmText={endUserName(user)}
        destructive
        loading={remove.isPending}
      />
    </div>
  );
}
