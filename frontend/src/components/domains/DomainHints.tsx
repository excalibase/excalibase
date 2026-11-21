import { secondaryButton } from '../containers/ContainerBits';

// Common two-part public suffixes, so example.co.uk reads as bare too. The
// server's Public Suffix List check is the real rule; this only warns early.
const TWO_PART_SUFFIXES = new Set([
  'co.uk', 'org.uk', 'ac.uk', 'com.au', 'net.au', 'org.au', 'co.nz', 'com.br', 'co.jp',
  'com.vn', 'com.sg', 'com.my', 'co.in', 'co.za', 'com.mx', 'com.tr', 'co.kr', 'com.cn',
]);

// The host typed, without scheme, path, port or trailing dot, lowercased.
function typedHost(raw: string): string {
  const host = raw.trim().toLowerCase().replace(/^[a-z]+:\/\//, '').split(/[/?#]/)[0] ?? '';
  return host.replace(/:\d+$/, '').replace(/\.$/, '');
}

// A bare (registered) domain such as example.com, which a CNAME cannot point at.
export function bareDomain(raw: string): string | null {
  const labels = typedHost(raw).split('.').filter(Boolean);
  if (labels.length < 2 || labels.some((label) => !/^[a-z0-9-]+$/.test(label))) return null;
  const suffixLabels = TWO_PART_SUFFIXES.has(labels.slice(-2).join('.')) ? 2 : 1;
  return labels.length === suffixLabels + 1 ? labels.join('.') : null;
}

export function SubdomainOnlyNotice({ testId }: { readonly testId: string }) {
  return (
    <div
      role="note"
      className="rounded border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200"
      data-testid={testId}
    >
      Only a subdomain can be added, such as <span className="font-mono">www.example.com</span>{' '}
      or <span className="font-mono">shop.example.com</span>. A bare domain like{' '}
      <span className="font-mono">example.com</span> can&apos;t point here: set up a redirect from
      it to the www address at your domain provider.
    </div>
  );
}

interface BareDomainWarningProps {
  readonly hostname: string;
  readonly onUse: (hostname: string) => void;
  readonly testId: string;
}

// Shown while the field holds a bare domain, with a button that turns it into its www form.
export function BareDomainWarning({ hostname, onUse, testId }: BareDomainWarningProps) {
  const bare = bareDomain(hostname);
  if (!bare) return null;
  return (
    <output className="flex flex-wrap items-center gap-2 text-xs text-amber-400" data-testid={testId}>
      <span>
        {bare} is a bare domain and can&apos;t be added. Add www.{bare} instead, and redirect {bare}{' '}
        to it at your domain provider.
      </span>
      <button type="button" className={secondaryButton} onClick={() => onUse(`www.${bare}`)}>
        Use www.{bare}
      </button>
    </output>
  );
}
