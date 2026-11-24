import { useState, type FormEvent } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { Building2 } from 'lucide-react';
import { acceptInvite } from '../../../api/orgs';
import { Button } from '../../Button';

// An invite arrives as a link with ?invite=<code>; a bare code works too.
export function inviteToken(input: string): string {
  const text = input.trim();
  if (!/^(https?:\/\/|\/)/.test(text)) return text;
  try {
    // A relative link is the server's own form; it is read against this site.
    return new URL(text, globalThis.location.origin).searchParams.get('invite') ?? '';
  } catch {
    return text;
  }
}

// What a person who belongs to no organization sees in place of the console's first pages.
export function NoOrganization() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [value, setValue] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [accepting, setAccepting] = useState(false);
  const token = inviteToken(value);

  const accept = async (event: FormEvent) => {
    event.preventDefault();
    setError(null);
    setAccepting(true);
    try {
      await acceptInvite(token);
      await queryClient.invalidateQueries({ queryKey: ['orgs'] });
      navigate('/orgs');
    } catch (err: unknown) {
      setError((err as { response?: { data?: { error?: string } } }).response?.data?.error ?? 'Could not accept the invite');
    } finally {
      setAccepting(false);
    }
  };

  return (
    <section className="mx-auto max-w-xl space-y-6 py-10" data-testid="no-organization">
      <div className="text-center">
        <Building2 className="mx-auto mb-3 h-10 w-10 text-text-tertiary" aria-hidden="true" />
        <h2 className="text-lg font-semibold text-text-primary">Join or create an organization</h2>
        <p className="mt-1 text-sm text-text-secondary">Projects belong to an organization. You are not in one yet.</p>
      </div>
      <div className="space-y-3 rounded-xl border border-border-primary bg-surface-card p-4">
        <h3 className="text-sm font-semibold text-text-primary">Start your own</h3>
        <Link to="/orgs?new=1" className="inline-flex min-h-9 items-center rounded-lg bg-purple-600 px-3 text-sm font-medium text-white hover:bg-purple-500">
          Create an organization
        </Link>
      </div>
      <form onSubmit={(e) => { void accept(e); }} className="space-y-3 rounded-xl border border-border-primary bg-surface-card p-4">
        <h3 className="text-sm font-semibold text-text-primary">Have an invite?</h3>
        <label htmlFor="invite-input" className="block text-sm text-text-secondary">Invite link or code</label>
        <input id="invite-input" value={value} onChange={(e) => setValue(e.target.value)} autoComplete="off"
          className="w-full rounded-lg border border-border-primary bg-bg-secondary px-3 py-2 text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500" />
        {error && <p role="alert" className="text-sm text-red-400">{error}</p>}
        <Button type="submit" disabled={accepting || !token}>{accepting ? 'Accepting...' : 'Accept invite'}</Button>
      </form>
    </section>
  );
}
