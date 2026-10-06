import { useRef, useState } from 'react';
import { AlertTriangle, Plus, X } from 'lucide-react';
import { ConfirmModal } from '../ui/ConfirmModal';
import { apiErrorMessage, type TrackedFunction } from '../../api/permissions';
import {
  useAddFunctionPermission,
  useRemoveFunctionPermission,
  useTrackFunction,
  useUntrackFunction,
} from '../../hooks/usePermissions';
import { jsonArguments, validatePermissionRole } from '../../utils/permissionRules';
import type { FunctionInfo } from '../../types/schema';

interface FunctionApiAccessProps {
  readonly projectId: string;
  readonly fn: FunctionInfo;
  readonly tracked: TrackedFunction | undefined;
  readonly roles: string[];
}

const badge = 'px-1.5 py-0.5 text-[10px] rounded font-medium';
const smallButton = 'px-2 py-1 text-xs rounded-md disabled:opacity-50';

const functionKey = (fn: FunctionInfo) => `${fn.schema || 'public'}.${fn.name}`;
const isDefiner = (fn: FunctionInfo) => /\bSECURITY\s+DEFINER\b/i.test(fn.definition ?? '');

/**
 * Whether a database function is reachable through the API (EXC-370 §6):
 * track or untrack it, and choose which roles may call it. Shown to
 * Developers and up; the server enforces the same.
 */
export function FunctionApiAccess({ projectId, fn, tracked, roles }: FunctionApiAccessProps) {
  const [definer, setDefiner] = useState(false);
  return (
    <div className="px-4 pb-3 pt-1 space-y-2 text-xs" data-testid={`fn-api-${fn.name}`}>
      {tracked ? (
        <TrackedView projectId={projectId} fn={fn} tracked={tracked} roles={roles} definer={definer || isDefiner(fn)} />
      ) : (
        <UntrackedView projectId={projectId} fn={fn} onTracked={setDefiner} />
      )}
    </div>
  );
}

interface UntrackedViewProps {
  readonly projectId: string;
  readonly fn: FunctionInfo;
  readonly onTracked: (securityDefiner: boolean) => void;
}

function UntrackedView({ projectId, fn, onTracked }: UntrackedViewProps) {
  const [open, setOpen] = useState(false);
  const [infer, setInfer] = useState(true);
  const [sessionArgument, setSessionArgument] = useState('');
  const [error, setError] = useState<string | null>(null);
  const track = useTrackFunction(projectId);
  const isQuery = fn.volatility !== 'VOLATILE';
  const jsonArgs = jsonArguments(fn.argTypes);

  const submit = () => {
    setError(null);
    track.mutate(
      { function: functionKey(fn), inferPermissions: isQuery ? infer : false, sessionArgument: sessionArgument || null },
      {
        onSuccess: (answer) => onTracked(answer.securityDefiner),
        onError: (err) => setError(apiErrorMessage(err, 'The function could not be tracked.')),
      },
    );
  };

  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <span className={`${badge} bg-text-tertiary/10 text-text-tertiary`}>Not in the API</span>
        {!open && (
          <button type="button" onClick={() => setOpen(true)} className={`${smallButton} text-purple-400 hover:bg-purple-500/10`}>
            Track
          </button>
        )}
      </div>
      {open && (
        <div className="rounded-lg border border-border-primary p-3 space-y-2">
          <p className="text-text-secondary">
            {isQuery
              ? 'Exposed as a query (it is STABLE or IMMUTABLE).'
              : 'Exposed as a mutation (it is VOLATILE); only roles you allow may call it.'}
          </p>
          {isQuery && (
            <label className="flex items-center gap-2 text-text-primary">
              <input type="checkbox" checked={infer} onChange={(e) => setInfer(e.target.checked)} className="rounded" />
              <span>Infer permissions from select</span>
            </label>
          )}
          {isQuery && (
            <p className="text-text-tertiary">
              {infer
                ? 'Every role that can select the rows it returns may call it.'
                : 'Only the roles you allow after tracking may call it.'}
            </p>
          )}
          {jsonArgs.length > 0 && (
            <div className="flex items-center gap-2 text-text-secondary">
              <label htmlFor={`session-arg-${fn.name}`}>Session argument</label>
              <select
                id={`session-arg-${fn.name}`}
                value={sessionArgument}
                onChange={(e) => setSessionArgument(e.target.value)}
                className="px-2 py-1 rounded border border-border-primary bg-bg-primary text-text-primary"
              >
                <option value="">none</option>
                {jsonArgs.map((name) => (
                  <option key={name} value={name}>
                    {name}
                  </option>
                ))}
              </select>
            </div>
          )}
          {jsonArgs.length > 0 && (
            <p className="text-text-tertiary">
              The session argument receives the request's session variables; clients cannot set it.
            </p>
          )}
          <div className="flex gap-2">
            <button
              type="button"
              onClick={submit}
              disabled={track.isPending}
              className={`${smallButton} bg-purple-500 hover:bg-purple-600 text-white`}
            >
              {track.isPending ? 'Tracking...' : 'Track function'}
            </button>
            <button type="button" onClick={() => setOpen(false)} className={`${smallButton} text-text-tertiary hover:bg-surface-hover`}>
              Cancel
            </button>
          </div>
          {error && (
            <p role="alert" className="text-red-400">
              {error}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

interface TrackedViewProps {
  readonly projectId: string;
  readonly fn: FunctionInfo;
  readonly tracked: TrackedFunction;
  readonly roles: string[];
  readonly definer: boolean;
}

function whoMayCall(tracked: TrackedFunction, returnType: string): string {
  if (tracked.exposedAs === 'QUERY' && tracked.inferPermissions) {
    return `Any role that can select the rows it returns (${returnType}) may call it, plus the roles listed.`;
  }
  if (tracked.exposedAs === 'MUTATION') {
    return 'Only the roles listed may call it, and only if they can also select the rows it returns.';
  }
  return 'Only the roles listed may call it.';
}

function TrackedView({ projectId, fn, tracked, roles, definer }: TrackedViewProps) {
  const [confirmUntrack, setConfirmUntrack] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const untrack = useUntrackFunction(projectId);
  const key = functionKey(fn);

  const handleUntrack = () => {
    setConfirmUntrack(false);
    setError(null);
    untrack.mutate(key, { onError: (err) => setError(apiErrorMessage(err, 'The function could not be untracked.')) });
  };

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className={`${badge} bg-emerald-500/10 text-emerald-400`}>
          Tracked · {tracked.exposedAs === 'QUERY' ? 'query' : 'mutation'}
        </span>
        {tracked.sessionArgument && (
          <span className="text-text-tertiary">Session argument: {tracked.sessionArgument}</span>
        )}
        <button
          type="button"
          onClick={() => setConfirmUntrack(true)}
          disabled={untrack.isPending}
          className={`${smallButton} text-red-400 hover:bg-red-500/10 ml-auto`}
        >
          Untrack
        </button>
      </div>
      {definer && (
        <p className="flex items-start gap-1.5 text-amber-400">
          <AlertTriangle className="w-3.5 h-3.5 flex-shrink-0 mt-px" />
          SECURITY DEFINER: runs with the owner's privileges. What its body reads or writes is not limited by API
          permissions.
        </p>
      )}
      <p className="text-text-tertiary">
        {whoMayCall(tracked, fn.returnType)} Rows it returns are filtered by the select permission of their table.
      </p>
      <FunctionRoles projectId={projectId} fnKey={key} name={fn.name} roles={roles} />
      {error && (
        <p role="alert" className="text-red-400">
          {error}
        </p>
      )}
      <ConfirmModal
        open={confirmUntrack}
        onClose={() => setConfirmUntrack(false)}
        onConfirm={handleUntrack}
        title="Untrack function"
        message={`${key} will no longer be reachable through the API, by any role.`}
        confirmLabel="Untrack"
        destructive
      />
    </div>
  );
}

interface FunctionRolesProps {
  readonly projectId: string;
  readonly fnKey: string;
  readonly name: string;
  readonly roles: string[];
}

function FunctionRoles({ projectId, fnKey, name, roles }: FunctionRolesProps) {
  const [role, setRole] = useState('');
  const [error, setError] = useState<string | null>(null);
  const add = useAddFunctionPermission(projectId);
  const remove = useRemoveFunctionPermission(projectId);
  // isPending reaches the button only after a render; a second Enter can land first.
  const adding = useRef(false);
  const failed = (err: Error) => setError(apiErrorMessage(err, 'The change could not be saved.'));

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const candidate = role.trim();
    const problem = validatePermissionRole(candidate) ?? (roles.includes(candidate) ? `${candidate} is already allowed` : null);
    setError(problem);
    if (problem || adding.current) return;
    adding.current = true;
    add.mutate(
      { function: fnKey, role: candidate },
      {
        onSuccess: () => setRole(''),
        onError: failed,
        onSettled: () => {
          adding.current = false;
        },
      },
    );
  };

  return (
    <div className="space-y-1">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-text-secondary">Allowed roles:</span>
        {roles.length === 0 && <span className="text-text-tertiary">none</span>}
        {roles.map((allowed) => (
          <span key={allowed} className="flex items-center gap-1 pl-1.5 pr-0.5 py-0.5 rounded border border-border-primary font-mono">
            {allowed}
            <button
              type="button"
              aria-label={`Remove ${allowed} from ${name}`}
              disabled={remove.isPending}
              onClick={() => {
                setError(null);
                remove.mutate({ function: fnKey, role: allowed }, { onError: failed });
              }}
              className="p-0.5 text-text-tertiary hover:text-red-400"
            >
              <X className="w-3 h-3" />
            </button>
          </span>
        ))}
      </div>
      <form onSubmit={submit} className="flex items-center gap-1.5">
        <input
          aria-label={`Role allowed to call ${name}`}
          value={role}
          onChange={(e) => setRole(e.target.value)}
          placeholder="e.g. editor"
          className="w-40 px-2 py-1 rounded border border-border-primary bg-bg-primary text-text-primary font-mono"
        />
        <button type="submit" disabled={add.isPending} className={`${smallButton} flex items-center gap-1 text-purple-400 hover:bg-purple-500/10`}>
          <Plus className="w-3 h-3" /> Allow role
        </button>
      </form>
      {error && (
        <p className="text-red-400" data-testid={`fn-role-error-${name}`}>
          {error}
        </p>
      )}
    </div>
  );
}
