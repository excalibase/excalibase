import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { cn } from '../../utils/cn';
import type { HealthCounts, SecuritySummary } from './model';
import type { Visit } from './recentlyVisited';
import { planLabel } from '../../hooks/useMemberOrgs';
import type { OrgUsage } from '../usage/usageModel';
import { projectsValue } from '../usage/UsageParts';
import type { CreditOverview } from '../../api/credits';
import { dollars, daysLeft } from '../usage/creditsFormat';
import { cpuText, memoryText, type PlanBudget } from '../../api/planBudget';

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

const LOW_CREDIT_SHARE = 0.2;

const dayCount = (days: number | null): string | undefined => {
  if (days === null) return undefined;
  return `${days} ${days === 1 ? 'day' : 'days'}`;
};

// What the Credits widget says beyond the balance: how long it lasts, and a warning when it runs low or out.
function creditNotes(credit: CreditOverview, now: Date): { expires?: string; warning?: string; offer?: string } {
  const left = daysLeft(credit.nextExpiry, now);
  const granted = credit.lots.reduce((sum, lot) => sum + lot.originalMicros, 0);
  let warning: string | undefined;
  if (credit.balanceMicros === 0 && credit.entries.length > 0) warning = 'Your credit is used up.';
  else if (granted > 0 && credit.balanceMicros < granted * LOW_CREDIT_SHARE) warning = 'Your credit is running low.';
  const offer = credit.trial.state === 'needs_terms' ? `Accept the credit terms to get ${dollars(credit.trial.amountMicros)}.` : undefined;
  return { expires: dayCount(left), warning, offer };
}

interface CreditsWidgetProps {
  readonly planName?: string;
  readonly projects?: OrgUsage['projects'];
  readonly limitsKnown: boolean;
  // Present only while the credits feature is on and the balance was read.
  readonly credit?: CreditOverview;
  readonly creditFailed?: boolean;
  // Present only while the plan budget is on and was read.
  readonly budget?: PlanBudget;
}

export function CreditsWidget({ planName, projects, limitsKnown, credit, creditFailed, budget }: CreditsWidgetProps) {
  const notes = credit ? creditNotes(credit, new Date()) : undefined;
  return (
    <Widget title="Credits & usage">
      {planName && projects ? (
        <div>
          {credit && <Row label="Credit balance" value={dollars(credit.balanceMicros)} warn={Boolean(notes?.warning)} />}
          {notes?.expires && <Row label="Credit lasts another" value={notes.expires} />}
          <Row label="Plan" value={planLabel(planName)} />
          <Row label="Projects" value={projectsValue(projects, limitsKnown)} />
          {budget && <Row label="CPU" value={`${cpuText(budget.used.cpuMilli)} of ${cpuText(budget.budget.cpuMilli)}`} warn={budget.left.cpuMilli <= 0} />}
          {budget && <Row label="Memory" value={`${memoryText(budget.used.memoryBytes)} of ${memoryText(budget.budget.memoryBytes)}`} warn={budget.left.memoryBytes <= 0} />}
        </div>
      ) : (
        <p className="text-text-secondary">Your plan and usage appear once you have an organization.</p>
      )}
      {notes?.warning && <output className="block text-amber-400">{notes.warning}</output>}
      {notes?.offer && <p className="text-text-secondary">{notes.offer}</p>}
      {creditFailed && <p role="alert" className="text-red-400">Could not read your credit.</p>}
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
  // A count that skipped databases it could not check would understate the risk, so none is shown.
  const openKnown = !openLoading && openUnchecked === 0;
  const tokenValue = (n: number) => (keysKnown ? n : '—');
  return (
    <Widget title="Security">
      <div>
        {summary.openDatabases !== null && (
          <Row label="Databases open to the internet" value={openKnown ? summary.openDatabases : '—'} warn={openKnown && summary.openDatabases > 0} />
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
