import { useState } from 'react';
import { Check, Copy } from 'lucide-react';

interface Props {
  email: string;
  link: string;
}

// The server returns the link's path only; the host is wherever Studio runs.
export function InviteLinkNotice({ email, link }: Readonly<Props>) {
  const url = `${window.location.origin}${link}`;
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState<string | null>(null);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(url);
      setCopied(true);
      setCopyError(null);
    } catch {
      setCopyError('Copy failed — select the link and copy it manually');
    }
  };

  return (
    <div className="p-4 bg-surface-card border border-purple-500/30 rounded-lg space-y-2" data-testid="invite-link-notice">
      <p className="text-sm text-text-primary">
        Send this one-time link to <span className="font-medium">{email}</span>. It works once and expires in 7 days.
        It cannot be shown again — invite the address again for a new link.
      </p>
      <div className="flex gap-2">
        <input
          readOnly
          value={url}
          data-testid="invite-link"
          aria-label="Invite link"
          onFocus={(e) => e.currentTarget.select()}
          className="flex-1 px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-xs font-mono"
        />
        <button
          type="button"
          onClick={copy}
          className="px-3 py-2 rounded-lg border border-border-primary text-sm text-text-secondary hover:text-text-primary flex items-center gap-1"
        >
          {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      {copyError && <p className="text-xs text-red-400">{copyError}</p>}
    </div>
  );
}
