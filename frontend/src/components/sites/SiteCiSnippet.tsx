import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Copy } from 'lucide-react';
import { API_BASE } from '../../api/base';
import { secondaryButton } from '../containers/ContainerBits';
import {
  siteCurlSnippet,
  siteGithubActionsSnippet,
  siteGitlabCiSnippet,
  siteJenkinsSnippet,
  type SiteSnippetTarget,
} from './siteCiSnippets';

interface CiKind {
  readonly id: string;
  readonly label: string;
  readonly file: string;
  // Where this CI keeps the token, so no snippet ever carries one.
  readonly secret: string;
  readonly render: (target: SiteSnippetTarget) => string;
}

const CI_KINDS: CiKind[] = [
  {
    id: 'github',
    label: 'GitHub Actions',
    file: '.github/workflows/deploy-site.yml',
    secret: 'a repository secret named EXCALIBASE_TOKEN',
    render: siteGithubActionsSnippet,
  },
  {
    id: 'gitlab',
    label: 'GitLab CI',
    file: '.gitlab-ci.yml',
    secret: 'a masked CI/CD variable named EXCALIBASE_TOKEN',
    render: siteGitlabCiSnippet,
  },
  {
    id: 'jenkins',
    label: 'Jenkins',
    file: 'Jenkinsfile',
    secret: 'a secret text credential with the ID excalibase-token',
    render: siteJenkinsSnippet,
  },
  {
    id: 'curl',
    label: 'Any CI',
    file: 'a step after the build',
    secret: 'a secret exposed to the step as EXCALIBASE_TOKEN',
    render: siteCurlSnippet,
  },
];

// The API as CI reaches it: absolute, whatever origin Studio is served from.
const absoluteApiUrl = () => new URL(API_BASE, globalThis.location.origin).href.replace(/\/+$/, '');

export function SiteCiSnippet({
  projectId,
  siteId,
}: {
  readonly projectId: string;
  readonly siteId: string;
}) {
  const [kindId, setKindId] = useState(CI_KINDS[0].id);
  const [copied, setCopied] = useState(false);
  const kind = CI_KINDS.find((candidate) => candidate.id === kindId) ?? CI_KINDS[0];
  const snippet = kind.render({ apiUrl: absoluteApiUrl(), projectId, siteId });

  const copy = async () => {
    await navigator.clipboard.writeText(snippet);
    setCopied(true);
  };

  return (
    <section className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex flex-wrap gap-1" role="tablist">
          {CI_KINDS.map((candidate) => (
            <button
              key={candidate.id}
              type="button"
              role="tab"
              aria-selected={candidate.id === kind.id}
              onClick={() => {
                setKindId(candidate.id);
                setCopied(false);
              }}
              className={`px-3 py-1.5 text-xs rounded-lg border ${
                candidate.id === kind.id
                  ? 'border-purple-500/50 bg-purple-500/10 text-text-primary'
                  : 'border-border-primary text-text-tertiary'
              }`}
              data-testid={`site-ci-tab-${candidate.id}`}
            >
              {candidate.label}
            </button>
          ))}
        </div>
        <button type="button" onClick={copy} className={secondaryButton} data-testid="site-ci-copy">
          <Copy className="w-3.5 h-3.5" /> {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <ol className="list-decimal list-inside text-sm text-text-secondary space-y-1">
        <li>
          <Link to="/account/tokens" className="text-purple-400 hover:underline">
            Create an access token
          </Link>{' '}
          with write access, bound to this project, and keep it as{' '}
          <span data-testid="site-ci-secret">{kind.secret}</span>.
        </li>
        <li>
          Add this as{' '}
          <span className="font-mono" data-testid="site-ci-file">
            {kind.file}
          </span>
          . It builds with npm, packs <span className="font-mono">dist</span> and uploads it; the
          upload goes live. Change the build step or folder if yours differ.
        </li>
      </ol>
      <pre
        className="text-xs font-mono bg-bg-tertiary border border-border-primary rounded-lg p-3 overflow-x-auto"
        data-testid="site-ci-snippet"
      >
        {snippet}
      </pre>
    </section>
  );
}
