import { useState } from 'react';
import { statementCsvHref, useAcceptCreditTerms, useOrgCredits, type CreditBurn, type CreditEntry, type CreditOverview } from '../../api/credits';
import { formatInstant } from '../../utils/instant';
import { serverErrorMessage } from '../../utils/serverError';
import { Line, Section } from './UsageParts';
import { dollars, daysLeft, recentMonths } from './creditsFormat';

const CELL = 'px-3 py-2 text-left';
const KIND_LABEL: Record<CreditEntry['kind'], string> = {
  grant_trial: 'Sign-up credit',
  grant_admin: 'Credit added',
  adjust_admin: 'Credit adjusted',
  usage: 'Usage',
  expire: 'Credit expired',
};
const STATEMENT_MONTHS = 6;

// What the credit could not pay of a usage line, in micros; 0 when it paid in full.
function uncoveredMicros(entry: CreditEntry): number {
  const value = entry.details?.uncovered_micros;
  return typeof value === 'number' ? value : 0;
}

function TrialOffer({ orgId, overview }: { readonly orgId: string; readonly overview: CreditOverview }) {
  const [agreed, setAgreed] = useState(false);
  const accept = useAcceptCreditTerms(orgId);
  const { state, position, amountMicros, days } = overview.trial;
  if (state === 'waitlisted') {
    return <p>You are number {position} in line for the {dollars(amountMicros)} sign-up credit. It is added when a place opens.</p>;
  }
  if (state === 'pending') return <p>Your {dollars(amountMicros)} sign-up credit is being added.</p>;
  if (state === 'refused') return <p>The sign-up credit is not available for this account.</p>;
  if (state !== 'needs_terms') return null;
  return (
    <div className="space-y-2">
      <p>Verified accounts get {dollars(amountMicros)} in credit for {days} days, once per person.</p>
      <label className="flex items-start gap-2">
        <input type="checkbox" checked={agreed} onChange={(event) => setAgreed(event.target.checked)} />
        <span>I accept the <a href={overview.termsUrl} target="_blank" rel="noreferrer" className="text-purple-400 hover:underline">credit terms</a>.</span>
      </label>
      <button
        type="button"
        disabled={!agreed || accept.isPending}
        onClick={() => accept.mutate(overview.termsVersion)}
        className="rounded-lg bg-purple-600 px-3 py-1.5 text-white disabled:opacity-50"
      >
        Accept and get {dollars(amountMicros)}
      </button>
      {accept.error && <p role="alert" className="text-red-400">{serverErrorMessage(accept.error, 'Could not record your acceptance')}</p>}
    </div>
  );
}

function History({ entries }: { readonly entries: readonly CreditEntry[] }) {
  if (entries.length === 0) return <p className="text-text-secondary">No credit activity yet.</p>;
  return (
    <table className="w-full text-text-primary">
      <thead className="text-text-secondary"><tr><th className={CELL}>When</th><th className={CELL}>What</th><th className={`${CELL} text-right`}>Amount</th></tr></thead>
      <tbody>
        {entries.map((entry) => (
          <tr key={entry.id} className="border-t border-border-primary">
            <td className={CELL}>{formatInstant(entry.periodStart ?? entry.createdAt)}</td>
            <td className={CELL}>{KIND_LABEL[entry.kind]}{entry.reason ? ` · ${entry.reason}` : ''}{uncoveredMicros(entry) > 0 && ` · Not covered: ${dollars(uncoveredMicros(entry))}`}</td>
            <td className={`${CELL} text-right`}>{dollars(entry.amountMicros)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const plural = (count: number) => `${count} day${count === 1 ? '' : 's'}`;

function BurnDays({ days }: { readonly days: CreditBurn['days'] }) {
  const largest = Math.max(1, ...days.map((day) => day.micros));
  return (
    <ul aria-label={`Burn over the last ${days.length} days`} className="grid grid-cols-7 gap-1 text-xs text-text-secondary">
      {days.map((day) => (
        <li key={day.day} className="flex flex-col items-center gap-1">
          <span className="flex h-12 w-full items-end"><span className="w-full rounded-sm bg-purple-500/70" style={{ height: `${Math.max(2, Math.round((day.micros / largest) * 100))}%` }} /></span>
          <span>{day.day}</span>
          <span className="text-text-primary">{dollars(day.micros)}</span>
        </li>
      ))}
    </ul>
  );
}

// The run-out line: a used-up credit has no run-out, a burn too slow to date says so, and only no burn says "No usage yet".
function runOutText(overview: CreditOverview): string {
  const { burn, balanceMicros } = overview;
  if (balanceMicros <= 0) return 'Credit used up';
  if (burn.runOutAt) return `${formatInstant(burn.runOutAt, 'date')} (${plural(daysLeft(burn.runOutAt, new Date()) ?? 0)})`;
  return burn.perDayMicros > 0 ? 'More than 10 years away' : 'No usage yet';
}

// What the org spends a day and when that runs out; nothing while usage is not billed.
function Burn({ overview }: { readonly overview: CreditOverview }) {
  if (!overview.metering) return <p className="text-text-secondary">Usage is not billed yet, so your balance only changes when credit is added or expires.</p>;
  const { burn, nextExpiry } = overview;
  const expiresFirst = burn.runOutAt !== null && nextExpiry !== null && Date.parse(nextExpiry) < Date.parse(burn.runOutAt);
  return (
    <>
      <Line label="Burn per day" value={dollars(burn.perDayMicros)} />
      <Line label="Projected run-out" value={runOutText(overview)} />
      {expiresFirst && <p className="text-text-secondary">Your credit expires before it runs out at this rate.</p>}
      <BurnDays days={burn.days} />
    </>
  );
}

function Statement({ orgId }: { readonly orgId: string }) {
  const months = recentMonths(new Date(), STATEMENT_MONTHS);
  const [month, setMonth] = useState(months[0]);
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label className="flex items-center gap-2">
        <span>Statement for</span>
        <select value={month} onChange={(event) => setMonth(event.target.value)} className="rounded-lg border border-border-primary bg-bg-secondary p-1.5 text-text-primary">
          {months.map((value) => <option key={value} value={value}>{value}</option>)}
        </select>
      </label>
      <a href={statementCsvHref(orgId, month)} download className="text-purple-400 hover:underline">Download CSV</a>
    </div>
  );
}

// The org's credit: balance, what expires when, the sign-up offer, history and the monthly statement.
export function CreditsSection({ orgId }: { readonly orgId: string }) {
  const { data, isLoading, error } = useOrgCredits(orgId, true);
  if (isLoading) return <p className="text-sm text-text-secondary">Loading credit…</p>;
  if (error || !data) return <p role="alert" className="rounded-xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-400">Could not load your credit. Reload the page to try again.</p>;
  const left = daysLeft(data.nextExpiry, new Date());
  return (
    <Section title="Credits">
      <Line label="Balance" value={dollars(data.balanceMicros)} />
      {data.nextExpiry && <Line label="Next credit expires" value={`${formatInstant(data.nextExpiry)} (${left} day${left === 1 ? '' : 's'} left)`} />}
      {data.lots.length > 1 && data.lots.map((lot) => (
        <Line key={lot.id} label={lot.expiresAt ? `Expires ${formatInstant(lot.expiresAt, 'date')}` : 'Does not expire'} value={dollars(lot.remainingMicros)} />
      ))}
      <Burn overview={data} />
      <TrialOffer orgId={orgId} overview={data} />
      <h3 className="pt-2 font-medium text-text-primary">History</h3>
      <History entries={data.entries} />
      <Statement orgId={orgId} />
      <p className="text-text-secondary">Times are shown in your local time. Billing periods and statements are in {data.billedIn}.</p>
    </Section>
  );
}
