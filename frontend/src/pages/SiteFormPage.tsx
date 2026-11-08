import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { apiErrorMessage } from '../api/apps';
import { useCreateSite } from '../api/sites';
import { primaryButton, secondaryButton } from '../components/containers/ContainerBits';
import { SitesGate, SitesHeader } from '../components/sites/SiteBits';
import { siteNameProblem } from '../components/sites/siteName';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';
import { useProjectRole } from '../hooks/useProjectRole';

function KindChoice({
  kind,
  onChange,
}: {
  readonly kind: 'static' | 'server';
  readonly onChange: (kind: 'static' | 'server') => void;
}) {
  const option = (value: 'static' | 'server', label: string) => (
    <label className="flex items-center gap-2 text-sm text-text-primary">
      <input
        type="radio"
        name="site-kind"
        checked={kind === value}
        onChange={() => onChange(value)}
        data-testid={`site-kind-${value}`}
      />
      {label}
    </label>
  );
  return (
    <fieldset className="space-y-2" data-testid="site-kind">
      <legend className="text-sm font-medium text-text-primary">Kind</legend>
      {option('static', 'Static files (Vite, Astro static, a Next.js export)')}
      {option('server', 'Server-rendered (Next.js)')}
      <p className="text-xs text-text-tertiary" data-testid="site-kind-hint">
        {kind === 'server'
          ? 'Next.js runs on the edge and is built from git. It cannot be changed later. An app that does not fit runs as a container instead.'
          : 'The built files are served from the CDN. It cannot be changed later.'}
      </p>
    </fieldset>
  );
}

function CreateSiteForm({ projectId }: { readonly projectId: string }) {
  const navigate = useNavigate();
  const create = useCreateSite(projectId);
  const [name, setName] = useState('');
  const [spaFallback, setSpaFallback] = useState(true);
  const { enabled: serverSitesOn } = useFeatureEnabled('sitesserver');
  const [kind, setKind] = useState<'static' | 'server'>('static');
  const server = serverSitesOn && kind === 'server';
  const problem = name === '' ? null : siteNameProblem(name);

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (name === '' || problem) return;
    create.mutate(server ? { name, kind: 'server', spaFallback: false } : { name, spaFallback }, {
      onSuccess: (site) => navigate(`/project/${projectId}/sites/${site.id}`),
    });
  };

  return (
    <form
      onSubmit={submit}
      className="bg-surface-card border border-border-primary rounded-lg p-6 space-y-5 max-w-xl"
    >
      <div className="space-y-1.5">
        <label htmlFor="site-name" className="text-sm font-medium text-text-primary">
          Name
        </label>
        <input
          id="site-name"
          value={name}
          onChange={(event) => setName(event.target.value)}
          placeholder="my-frontend"
          autoComplete="off"
          className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
          data-testid="site-name"
        />
        <p className="text-xs text-text-tertiary">
          The first part of the site&apos;s address. It cannot be changed later.
        </p>
        {problem && (
          <p className="text-xs text-red-400" data-testid="site-name-error">
            {problem}
          </p>
        )}
      </div>
      {serverSitesOn && <KindChoice kind={kind} onChange={setKind} />}
      {!server && (
        <label className="flex items-start gap-3 text-sm text-text-primary">
          <input
            type="checkbox"
            checked={spaFallback}
            onChange={(event) => setSpaFallback(event.target.checked)}
            className="mt-0.5"
            data-testid="site-spa-fallback"
          />
          <span>
            Single-page app
            <span className="block text-xs text-text-tertiary">
              Unknown paths load index.html so client-side routes work. Off, they get your 404.html.
            </span>
          </span>
        </label>
      )}
      {create.error && (
        <div
          role="alert"
          className="rounded-lg border border-red-500/30 bg-red-500/10 px-4 py-3 text-sm text-red-400"
        >
          {apiErrorMessage(create.error, 'The site could not be created')}
        </div>
      )}
      <div className="flex gap-2">
        <button
          type="submit"
          disabled={name === '' || !!problem || create.isPending}
          className={primaryButton}
          data-testid="site-create"
        >
          Create site
        </button>
        <button
          type="button"
          onClick={() => navigate(`/project/${projectId}/sites`)}
          className={secondaryButton}
        >
          Cancel
        </button>
      </div>
    </form>
  );
}

function CreateSiteOrNotice({ projectId }: { readonly projectId: string }) {
  const { canDevelop, isLoading } = useProjectRole(projectId);
  if (isLoading) return null;
  if (!canDevelop) {
    return (
      <p className="text-sm text-text-secondary" data-testid="site-create-not-allowed">
        Creating a site takes the developer role in this project&apos;s organization.
      </p>
    );
  }
  return <CreateSiteForm projectId={projectId} />;
}

export function SiteFormPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  return (
    <div data-testid="site-form-page">
      <Link
        to={`/project/${projectId}/sites`}
        className="text-xs text-text-tertiary hover:text-text-primary"
      >
        ← Sites
      </Link>
      <div className="mt-3">
        <SitesHeader
          title="New site"
          subtitle="A name for its address, and how unknown paths are answered."
        />
        <SitesGate projectId={projectId}>
          <CreateSiteOrNotice projectId={projectId} />
        </SitesGate>
      </div>
    </div>
  );
}
