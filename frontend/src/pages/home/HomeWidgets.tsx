import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { cn } from '../../utils/cn';
import type { HealthCounts, SecuritySummary } from './model';
import type { Visit } from './recentlyVisited';

export function Widget({ title, children }: { readonly title: string; readonly children: ReactNode }) {
  return (
    <section aria-label={title} data-testid={`widget-${title.toLowerCase().replaceAll(/[^a-z]+/g, '-')}`} className="flex flex-col gap-3 rounded-xl border border-border-primary bg-surface-card p-4 text-sm">
      <h2 className="text-[15px] font-semibold text-text-primary">{title}</h2>
      {children}
    </section>
  );
}

const MORE = 'text-purple-400 hover:underline focus-visible:underline';

export function RecentlyVisitedWidget({ visits }: { readonly visits: readonly Visit[] }) {
  return (
    <Widget title="Recently visited">
      {visits.length === 0 ? (
        <p className="text-text-secondary">Pages you open will appear here.</p>
      ) : (
        <ul>
          {visits.map((visit) => (
            <li key={visit.to} className="border-b border-border-primary last:border-0">
              <Link to={visit.to} className="flex justify-between gap-3 py-2 text-text-primary hover:underline">
                <span>{visit.title}</span><span className="text-text-secondary">{visit.kind}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
      <Link to="/instances" className={MORE}>All projects →</Link>
    </Widget>
  );
}

function Stat({ label, value, tone }: { readonly label: string; readonly value: number; readonly tone?: string }) {
  return (
    <div>
      <div className="text-xs text-text-secondary">{label}</div>
      <div className={cn('text-2xl font-semibold text-text-primary', tone)}>{value}</div>
    </div>
  );
}

export function HealthWidget({ counts, failingApps }: { readonly counts: HealthCounts; readonly failingApps: number | null }) {
  return (
    <Widget title="Health">
      <div className="grid grid-cols-3 gap-2">
        <Stat label="Running" value={counts.running} />
        <Stat label="Paused" value={counts.paused} tone={counts.paused > 0 ? 'text-amber-400' : undefined} />
        <Stat label="Failing" value={counts.failing} tone={counts.failing > 0 ? 'text-red-400' : undefined} />
      </div>
      {counts.inProgress > 0 && <p className="text-text-secondary">{counts.inProgress} changing state now</p>}
      {failingApps !== null && <p className="text-text-secondary">Apps failing: {failingApps}</p>}
    </Widget>
  );
}

// No credit or usage figures are served yet, so none are drawn.
export function CreditsWidget() {
  return (
    <Widget title="Credits & usage">
      <p className="text-text-secondary">Credits left and spend by service are not shown on Home yet. The usage page has the details that exist today.</p>
      <Link to="/usage" className={MORE}>Usage details →</Link>
    </Widget>
  );
}

function Row({ label, value, warn, to }: { readonly label: string; readonly value: ReactNode; readonly warn?: boolean; readonly to?: string }) {
  const content = (
    <>
      <span>{label}</span>
      <span className={cn('font-medium', warn && 'text-amber-400')}>{value}</span>
    </>
  );
  const row = 'flex justify-between gap-3 border-b border-border-primary py-1.5 last:border-0';
  return to ? <Link to={to} className={cn(row, 'text-text-primary hover:underline')}>{content}</Link> : <div className={cn(row, 'text-text-primary')}>{content}</div>;
}

interface SecurityProps {
  readonly summary: Omit<SecuritySummary, 'openDatabases'> & { openDatabases: number | null };
  readonly openUnchecked: number;
  readonly keysKnown: boolean;
  // The projects or their allow-lists are still loading, or could not be read: no count is claimed.
  readonly openLoading: boolean;
}

export function SecurityWidget({ summary, openUnchecked, keysKnown, openLoading }: SecurityProps) {
  const tokenValue = (n: number) => (keysKnown ? n : '—');
  return (
    <Widget title="Security">
      <div>
        {summary.openDatabases !== null && (
          <Row label="Databases open to the internet" value={openLoading ? '—' : summary.openDatabases} warn={!openLoading && summary.openDatabases > 0} />
        )}
        <Row label="Keys with access to every project" value={tokenValue(summary.allProjectTokens)} warn={keysKnown && summary.allProjectTokens > 0} to="/account/tokens" />
        <Row label="Keys expiring in 60 days" value={tokenValue(summary.expiringTokens)} to="/account/tokens" />
      </div>
      {openUnchecked > 0 && <p className="text-text-secondary">{openUnchecked} database{openUnchecked === 1 ? '' : 's'} could not be checked.</p>}
    </Widget>
  );
}

export interface BuildLink { to: string; label: string }

export function BuildWidget({ links, hint }: { readonly links: readonly BuildLink[]; readonly hint?: string }) {
  return (
    <Widget title="Build something">
      <ul className="space-y-2">
        {links.map((link) => <li key={link.to}><Link to={link.to} className={MORE}>{link.label}</Link></li>)}
      </ul>
      {hint && <p className="text-text-secondary">{hint}</p>}
    </Widget>
  );
}
