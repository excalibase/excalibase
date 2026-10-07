import { Globe, Loader2 } from 'lucide-react';
import { useAddCorsOrigin, useClearCorsWildcard, useProjectCors, useRemoveCorsOrigin } from '../api/projectCors';
import { serverErrorMessage } from '../utils/serverError';
import { AllowList } from './AllowList';
import { Button } from './Button';

// CorsOriginsCard lists and edits the browser origins the project's API
// answers. A page on any other origin gets an opaque CORS failure.
export function CorsOriginsCard({ projectId }: { readonly projectId: string }) {
  const cors = useProjectCors(projectId);
  const add = useAddCorsOrigin(projectId);
  const remove = useRemoveCorsOrigin(projectId);
  const clearWildcard = useClearCorsWildcard(projectId);
  const busy = add.isPending || remove.isPending || clearWildcard.isPending;

  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid="cors-origins-card">
      <h4 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-2">
        <Globe className="w-4 h-4" /> Allowed origins
      </h4>
      <p className="text-xs text-text-secondary mb-2">
        Browser apps can call this project's API only from these origins: the exact scheme, host and port the
        page runs on, with no path. For local development add <code>http://localhost:&lt;port&gt;</code>, for
        example <code>http://localhost:5173</code>. Native apps send their own scheme:{' '}
        <code>capacitor://localhost</code>, <code>ionic://localhost</code>, <code>tauri://localhost</code> (or{' '}
        <code>http://tauri.localhost</code> on Windows).
      </p>
      <p className="text-xs text-text-tertiary mb-3">A change reaches the API within about a minute.</p>

      {cors.isLoading && <Loader2 className="w-4 h-4 animate-spin" />}
      {cors.isError && (
        <p className="text-xs text-red-400" data-testid="cors-origins-error">
          {serverErrorMessage(cors.error, 'The allowed origins could not be read')}
        </p>
      )}
      {cors.data?.allowWildcard && (
        <div className="mb-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs" data-testid="cors-wildcard">
          <p className="text-text-primary mb-2">Every origin is allowed (<code>*</code>): any website can call this API from a browser.</p>
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => clearWildcard.mutate()}>
            Stop allowing every origin
          </Button>
        </div>
      )}
      {cors.data && !cors.data.allowWildcard && (
        <AllowList
          name="cors-origins"
          entries={cors.data.allowedOrigins}
          emptyText="No browser origin is allowed yet, so browser apps get CORS errors."
          inputLabel="Origin"
          placeholder="http://localhost:5173"
          addLabel="Add origin"
          busy={busy}
          error={add.error ?? remove.error}
          onAdd={(origin, done) => {
            remove.reset();
            add.mutate(origin, { onSuccess: done });
          }}
          onRemove={(origin) => {
            add.reset();
            remove.mutate(origin);
          }}
        />
      )}
      {clearWildcard.error != null && (
        <p role="alert" className="text-xs text-red-400 mt-2 break-words">
          {serverErrorMessage(clearWildcard.error, 'The change was not made')}
        </p>
      )}
    </div>
  );
}
