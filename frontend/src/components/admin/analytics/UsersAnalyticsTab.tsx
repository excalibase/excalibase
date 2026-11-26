import type { UserAnalytics } from '../../../api/adminAnalytics';
import { useUserAnalytics } from '../../../hooks/useAdminAnalytics';
import { BarChart } from './BarChart';
import { domainVerdict } from './domainPill';
import { KpiRow, Lede, Loaded, Panel, Pill, shortDay } from './AnalyticsKit';

const percent = (part: number, whole: number) => (whole === 0 ? '0%' : `${Math.round((part / whole) * 100)}%`);

function Funnel({ steps }: { readonly steps: UserAnalytics['funnel'] }) {
  const base = steps[0]?.count ?? 0;
  return (
    <ul className="space-y-2">
      {steps.map((step) => (
        <li key={step.label} className="grid grid-cols-[8rem_minmax(0,1fr)_4.5rem] items-center gap-2 text-sm">
          <span className="text-text-secondary">{step.label}</span>
          <div className="h-5 rounded bg-bg-secondary overflow-hidden">
            <div className="h-full bg-accent-primary" style={{ width: base === 0 ? '0%' : `${(step.count / base) * 100}%` }} />
          </div>
          <span className="text-right tabular-nums">{step.count} <small className="text-text-tertiary">{percent(step.count, base)}</small></span>
        </li>
      ))}
    </ul>
  );
}

function Domains({ domains }: { readonly domains: UserAnalytics['domains'] }) {
  if (domains.length === 0) return <p className="text-sm text-text-tertiary">No sign-ups in the window.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-text-tertiary border-b border-border-primary">
            <th className="py-2 font-medium">Domain</th>
            <th className="py-2 text-right font-medium">Accounts</th>
            <th className="py-2 text-right font-medium">Suspended</th>
            <th className="py-2" />
          </tr>
        </thead>
        <tbody>
          {domains.map((stat) => {
            const verdict = domainVerdict(stat);
            return (
              <tr key={stat.domain} className="border-b border-border-primary last:border-b-0">
                <td className="py-2">{stat.domain}</td>
                <td className="py-2 text-right tabular-nums">{stat.accounts}</td>
                <td className="py-2 text-right tabular-nums">{stat.suspended}</td>
                <td className="py-2 text-right">{verdict && <Pill tone={verdict === 'spam' ? 'bad' : 'warn'}>{verdict}</Pill>}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function Body({ data }: { readonly data: UserAnalytics }) {
  const kept = data.perDay.map((day) => day.total - (day.flagged ?? 0));
  const suspended = data.perDay.map((day) => day.flagged ?? 0);
  const real = data.funnel.at(-1)?.count ?? 0;
  return (
    <div className="space-y-4">
      <KpiRow items={[
        { label: 'Sign-ups', value: data.signups, note: `last ${data.windowDays} days` },
        { label: 'Never refused', value: real, note: `${percent(real, data.signups)} of sign-ups` },
        { label: 'Active, last 7 days', value: data.activeLast7Days, note: 'signed in, not suspended' },
        { label: 'Later suspended', value: data.suspended, note: `${percent(data.suspended, data.signups)} of sign-ups`, tone: data.suspended > 0 ? 'bad' : 'neutral' },
      ]}
      />
      <Panel
        title={`Sign-ups per day, last ${data.windowDays} days`}
        hint="Hover a bar for the day's figures."
        aside={(
          <div className="flex gap-3 text-xs text-text-secondary">
            <span><i className="inline-block w-2.5 h-2.5 mr-1 rounded-sm bg-accent-primary" />kept</span>
            <span><i className="inline-block w-2.5 h-2.5 mr-1 rounded-sm bg-color-error" />later suspended</span>
          </div>
        )}
      >
        <BarChart
          label="Sign-ups per day" labels={data.perDay.map((day) => shortDay(day.day))} series={[kept, suspended]}
          seriesClasses={['fill-accent-primary', 'fill-color-error']} every={3}
          tip={(i) => `${data.perDay[i].total} sign-ups, ${suspended[i]} suspended`}
        />
      </Panel>
      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Panel title="Funnel" hint={`Of the ${data.signups} sign-ups in the window.`}><Funnel steps={data.funnel} /></Panel>
        <Panel title="Top sign-up domains" hint="Domains where most accounts were suspended are marked."><Domains domains={data.domains} /></Panel>
      </div>
    </div>
  );
}

export function UsersAnalyticsTab() {
  const query = useUserAnalytics(true);
  return (
    <div className="space-y-4">
      <Lede title="Sign-ups">Customer sign-ups only; staff and service accounts are left out. A real customer has never had a deploy refused.</Lede>
      <Loaded query={query}>{(data) => <Body data={data} />}</Loaded>
    </div>
  );
}
