import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Copy } from 'lucide-react';
import { API_BASE } from '../../api/base';
import { githubActionsSnippet, type SnippetTarget } from './ciSnippets';
import { secondaryButton } from './ContainerBits';

interface CiKind {
  readonly id: string;
  readonly label: string;
  readonly file: string;
  readonly render: (target: SnippetTarget) => string;
}

const CI_KINDS: CiKind[] = [
  { id: 'github', label: 'GitHub Actions', file: '.github/workflows/deploy.yml', render: githubActionsSnippet },
];

// The API as CI reaches it: absolute, whatever origin Studio is served from.
const absoluteApiUrl = () => new URL(API_BASE, globalThis.location.origin).href.replace(/\/+$/, '');

export function CiSetup({ projectId, appId, image }: { readonly projectId: string; readonly appId: string; readonly image: string }) {
  const [kindId, setKindId] = useState(CI_KINDS[0].id);
  const [copied, setCopied] = useState(false);
  const kind = CI_KINDS.find((candidate) => candidate.id === kindId) ?? CI_KINDS[0];
  const snippet = kind.render({ apiUrl: absoluteApiUrl(), projectId, appId, image });

  const copy = async () => {
    await navigator.clipboard.writeText(snippet);
    setCopied(true);
  };

  return (
    <section className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3">
      <ol className="list-decimal list-inside text-sm text-text-secondary space-y-1">
        <li>
          <Link to="/account/tokens" className="text-purple-400 hover:underline" data-testid="ci-token-link">
            Create an access token
          </Link>{' '}
          with write access, bound to this project, and save it in your CI as the secret{' '}
          <code className="font-mono">EXCALIBASE_TOKEN</code>.
        </li>
        <li>If the image is private, save a registry credential for its registry under Containers.</li>
        <li>
          Add this as <code className="font-mono">{kind.file}</code>. Each push builds the image, pushes
          it, deploys its digest with the commit, and waits until it is live.
        </li>
      </ol>
      <div className="flex items-center justify-between gap-2">
        <div className="flex gap-1" role="tablist">
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
              data-testid={`ci-tab-${candidate.id}`}
            >
              {candidate.label}
            </button>
          ))}
        </div>
        <button type="button" onClick={copy} className={secondaryButton} data-testid="ci-copy">
          <Copy className="w-3.5 h-3.5" /> {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre
        className="text-xs font-mono bg-bg-tertiary border border-border-primary rounded-lg p-3 overflow-x-auto"
        data-testid="ci-snippet"
      >
        {snippet}
      </pre>
    </section>
  );
}
