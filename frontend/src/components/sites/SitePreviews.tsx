import { useState } from 'react';
import { apiErrorMessage } from '../../api/apps';
import {
  useRemovePreview,
  useSetPreviewAccess,
  useSetPreviewMode,
  useSitePreviews,
  type PreviewAccess,
  type PreviewMode,
  type PreviewStatus,
  type SitePreview,
} from '../../api/sitePreviews';
import { useFeatureEnabled } from '../../hooks/useDeploymentMode';
import { Spinner, secondaryButton } from '../containers/ContainerBits';

interface SitePreviewsProps {
  readonly projectId: string;
  readonly siteId: string;
  readonly productionBranch: string;
  readonly canDevelop: boolean;
  readonly canAdmin: boolean;
}

const card = 'bg-surface-card border border-border-primary rounded-lg p-4 space-y-3';

const STATUS: Record<PreviewStatus, { label: string; tone: string }> = {
  BUILDING: { label: 'Building', tone: 'text-amber-400' },
  READY: { label: 'Ready', tone: 'text-emerald-400' },
  FAILED: { label: 'Failed', tone: 'text-red-400' },
  REMOVING: { label: 'Removing', tone: 'text-text-tertiary' },
};

// Each opt-in reaches past the preview itself, so each says what it opens.
const ACCESS: { key: keyof PreviewAccess; label: string; warning: string }[] = [
  {
    key: 'forks',
    label: 'Build pull requests from forks',
    warning: 'Anyone who opens a pull request runs a build and gets a link. Fork builds never see project secrets.',
  },
  {
    key: 'secrets',
    label: 'Give previews the build-visible secrets',
    warning: 'Every branch and pull request in this repository builds with them and they can end up in the preview files.',
  },
  {
    key: 'apiAccess',
    label: "Let previews call this project's API",
    warning: "Adds each preview's address to the project's CORS list, so unreviewed code can use the production database.",
  },
  {
    key: 'publicLinks',
    label: 'Post preview links on public repositories',
    warning: 'Anyone who can read the repository can open the previews.',
  },
];

function PreviewRow({
  preview,
  canDevelop,
  onRemove,
  removing,
}: {
  readonly preview: SitePreview;
  readonly canDevelop: boolean;
  readonly onRemove: () => void;
  readonly removing: boolean;
}) {
  const status = STATUS[preview.status];
  const source = preview.source === 'pull-request' ? `Pull request #${preview.ref}` : `Branch ${preview.ref}`;
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 py-2" data-testid={`preview-${preview.id}`}>
      <div className="min-w-0 space-y-0.5">
        <p className="text-sm text-text-primary truncate">{source}</p>
        <a
          href={`https://${preview.hostname}`}
          target="_blank"
          rel="noopener noreferrer"
          className="text-xs text-blue-400 hover:underline break-all"
          data-testid={`preview-link-${preview.id}`}
        >
          {preview.hostname}
        </a>
        <p className="text-xs text-text-tertiary">
          <span className={status.tone}>{status.label}</span>
          {preview.commitSha && ` · ${preview.commitSha.slice(0, 7)}`}
          {preview.failureReason && ` · ${preview.failureReason}`}
        </p>
      </div>
      {canDevelop && preview.status !== 'REMOVING' && (
        <button
          type="button"
          className={secondaryButton}
          disabled={removing}
          onClick={onRemove}
          data-testid={`preview-remove-${preview.id}`}
        >
          Remove
        </button>
      )}
    </li>
  );
}

function ModeSelect({
  projectId,
  siteId,
  mode,
  productionBranch,
  canDevelop,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly mode: PreviewMode;
  readonly productionBranch: string;
  readonly canDevelop: boolean;
}) {
  const setMode = useSetPreviewMode(projectId, siteId);
  const [confirmOff, setConfirmOff] = useState(false);
  const choose = (next: PreviewMode) => {
    if (next === 'off' && mode !== 'off') {
      setConfirmOff(true);
      return;
    }
    setMode.mutate(next);
  };
  return (
    <div className="space-y-2">
      <label className="block text-xs text-text-secondary" htmlFor="preview-mode">
        Previews
      </label>
      <select
        id="preview-mode"
        className="bg-surface-input border border-border-primary rounded px-2 py-1 text-sm text-text-primary"
        value={mode}
        disabled={!canDevelop || setMode.isPending}
        onChange={(event) => choose(event.target.value as PreviewMode)}
        data-testid="preview-mode"
      >
        <option value="off">Off</option>
        <option value="pull-requests">One per open pull request</option>
        <option value="branches">One per branch, except {productionBranch}</option>
      </select>
      {confirmOff && (
        <div className="flex flex-wrap items-center gap-2" data-testid="preview-off-confirm">
          <p className="text-xs text-amber-400">Turning previews off removes every preview of this site.</p>
          <button
            type="button"
            className={secondaryButton}
            onClick={() => {
              setConfirmOff(false);
              setMode.mutate('off');
            }}
            data-testid="preview-off-confirm-button"
          >
            Turn off
          </button>
          <button type="button" className={secondaryButton} onClick={() => setConfirmOff(false)}>
            Keep
          </button>
        </div>
      )}
      {setMode.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(setMode.error, 'The preview mode could not be saved')}
        </p>
      )}
    </div>
  );
}

// accessOf keeps only the opt-ins, so the mode never rides along on an Admin's change.
const accessOf = ({ forks, secrets, apiAccess, publicLinks }: PreviewAccess): PreviewAccess => ({
  forks,
  secrets,
  apiAccess,
  publicLinks,
});

function AccessSwitches({
  projectId,
  siteId,
  access,
  canAdmin,
}: {
  readonly projectId: string;
  readonly siteId: string;
  readonly access: PreviewAccess;
  readonly canAdmin: boolean;
}) {
  const setAccess = useSetPreviewAccess(projectId, siteId);
  return (
    <fieldset className="space-y-2">
      <legend className="text-xs text-text-secondary">What previews may reach</legend>
      {!canAdmin && (
        <p className="text-xs text-text-tertiary" data-testid="preview-access-note">
          Only an Admin of the project changes these.
        </p>
      )}
      {ACCESS.map(({ key, label, warning }) => (
        <div key={key} className="space-y-0.5">
          <label className="flex items-center gap-2 text-sm text-text-primary">
            <input
              type="checkbox"
              checked={access[key]}
              disabled={!canAdmin || setAccess.isPending}
              onChange={(event) => setAccess.mutate({ ...accessOf(access), [key]: event.target.checked })}
              data-testid={`preview-access-${key}`}
            />
            {label}
          </label>
          <p className="text-xs text-text-tertiary pl-6" data-testid={`preview-access-${key}-warning`}>
            {warning}
          </p>
        </div>
      ))}
      {setAccess.error && (
        <p role="alert" className="text-xs text-red-400">
          {apiErrorMessage(setAccess.error, 'The change could not be saved')}
        </p>
      )}
    </fieldset>
  );
}

// Previews (EXC-660): hidden until the server turns sitepreviews on.
export function SitePreviews({ projectId, siteId, productionBranch, canDevelop, canAdmin }: SitePreviewsProps) {
  const { enabled } = useFeatureEnabled('sitepreviews');
  const { data, error } = useSitePreviews(projectId, siteId, enabled);
  const remove = useRemovePreview(projectId, siteId);
  if (!enabled) return null;
  return (
    <section className="space-y-2" data-testid="site-previews">
      <h4 className="text-sm font-semibold text-text-primary">Previews</h4>
      <div className={card}>
        {error && (
          <p className="text-sm text-text-secondary">{apiErrorMessage(error, 'Previews are not available here')}</p>
        )}
        {!error && !data && <Spinner />}
        {data && (
          <>
            <p className="text-xs text-text-tertiary">
              Each preview has its own hard-to-guess link, kept out of search engines. By default it gets no project
              secrets and cannot call this project's API.
            </p>
            <ModeSelect
              projectId={projectId}
              siteId={siteId}
              mode={data.settings.mode}
              productionBranch={productionBranch}
              canDevelop={canDevelop}
            />
            <AccessSwitches projectId={projectId} siteId={siteId} access={data.settings} canAdmin={canAdmin} />
            <p className="text-xs text-text-secondary" data-testid="previews-usage">
              {data.used} of {data.max} previews in use across the project
            </p>
            {data.previews.length > 0 && (
              <ul className="divide-y divide-border-primary">
                {data.previews.map((preview) => (
                  <PreviewRow
                    key={preview.id}
                    preview={preview}
                    canDevelop={canDevelop}
                    removing={remove.isPending}
                    onRemove={() => remove.mutate(preview.id)}
                  />
                ))}
              </ul>
            )}
            {remove.error && (
              <p role="alert" className="text-xs text-red-400">
                {apiErrorMessage(remove.error, 'The preview could not be removed')}
              </p>
            )}
          </>
        )}
      </div>
    </section>
  );
}
