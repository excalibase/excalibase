import { Link } from 'react-router-dom';
import { Check, ChevronDown, Plus } from 'lucide-react';
import { useCurrentOrg, planLabel } from '../../../hooks/useMemberOrgs';
import { useOrgSelection } from '../../../stores/org-selection';
import { usePopup } from './TopBar';

const BUTTON = 'inline-flex items-center gap-1.5 rounded-lg border border-border-primary bg-bg-secondary px-3 min-h-9 text-sm text-text-primary';

const MENU_LINK = 'flex items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-secondary hover:bg-surface-hover hover:text-text-primary';

// Offers only the orgs the person belongs to (see memberOrgsOf).
export function OrgSwitcher() {
  const { org, orgs, isLoading, error } = useCurrentOrg();
  const select = useOrgSelection((s) => s.select);
  const { open, setOpen, ref } = usePopup();

  if (isLoading) return <output className={BUTTON}>Loading organization</output>;
  if (error) return <span className={BUTTON} role="alert">Organization unavailable</span>;
  if (!org) return <Link to="/orgs?new=1" className={BUTTON}>No organization · Create one</Link>;

  return (
    <div ref={ref} className="relative">
      <button type="button" aria-haspopup="listbox" aria-expanded={open} aria-label={`Organization: ${org.name}, ${planLabel(org.tier)} plan`}
        onClick={() => setOpen(!open)} className={BUTTON}>
        {org.name} <span className="text-text-tertiary">· {planLabel(org.tier)}</span>
        <ChevronDown className="w-3.5 h-3.5" aria-hidden="true" />
      </button>
      {open && (
        <div className="absolute left-0 top-full z-30 mt-1 w-64 rounded-xl border border-border-primary bg-surface-card p-1 shadow-lg">
        <ul role="listbox" aria-label="Organizations" className="max-h-72 overflow-y-auto">
          {orgs.map((o) => (
            <li key={o.id} role="option" aria-selected={o.id === org.id} tabIndex={0}
              onClick={() => { select(o.id); setOpen(false); }}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); select(o.id); setOpen(false); } }}
              className="flex cursor-pointer items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-secondary hover:bg-surface-hover hover:text-text-primary">
              <span className="flex-1 truncate">{o.name}</span>
              <span className="text-xs text-text-tertiary">{planLabel(o.tier)}</span>
              {o.id === org.id && <Check className="w-4 h-4 text-purple-400" aria-hidden="true" />}
            </li>
          ))}
        </ul>
        <div className="mt-1 border-t border-border-primary pt-1">
          <Link to="/orgs" onClick={() => setOpen(false)} className={MENU_LINK}>All organizations</Link>
          <Link to="/orgs?new=1" onClick={() => setOpen(false)} className={MENU_LINK}><Plus className="w-4 h-4" aria-hidden="true" /> Create organization</Link>
        </div>
        </div>
      )}
    </div>
  );
}
