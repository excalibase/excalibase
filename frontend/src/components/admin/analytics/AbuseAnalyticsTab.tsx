import type { AbuseAnalytics } from '../../../api/adminAnalytics';
import { useAbuseAnalytics } from '../../../hooks/useAdminAnalytics';
import { BarChart } from './BarChart';
import { KpiRow, Lede, Loaded, Notice, Panel, Pill, shortDay } from './AnalyticsKit';

const HEADERS = ['When (UTC)', 'Account', 'Project', 'What it tried', 'Account now'];

function Latest({ rows }: { readonly rows: AbuseAnalytics['latest'] }) {
  if (rows.length === 0) return <p className="text-sm text-text-tertiary">No refused deploys in the window.</p>;
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-left text-xs text-text-tertiary border-b border-border-primary">
            {HEADERS.map((header) => <th key={header} className="py-2 font-medium">{header}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={`${row.at}-${row.projectId}-${row.kind}`} className="border-b border-border-primary last:border-b-0 align-top">
              <td className="py-2 whitespace-nowrap">{row.at.slice(5, 16).replace('T', ' ')}</td>
              <td className="py-2">{row.email}</td>
              <td className="py-2 font-mono text-xs">{row.projectId}</td>
              <td className="py-2 break-all"><Pill tone="neutral">{row.kind}</Pill> <span className="text-text-secondary">{row.details}</span></td>
              <td className="py-2">{row.suspended ? <Pill tone="good">suspended</Pill> : <Pill tone="bad">active</Pill>}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Body({ data }: { readonly data: AbuseAnalytics }) {
  const users = data.perDay.map((day) => day.flagged ?? 0);
  return (
    <div className="space-y-4">
      {data.refusedStillActive > 0 && (
        <Notice tone="bad" title={`${data.refusedStillActive} accounts with refused deploys are still active.`}>
          <p>A refusal stops the app, not the account, so they can keep trying.</p>
          <p className="text-text-primary">Next step: review them under Users and suspend the ones that keep retrying.</p>
        </Notice>
      )}
      <KpiRow items={[
        { label: 'Refused, still active', value: data.refusedStillActive, note: `of ${data.refusedUsers} refused users`, tone: 'bad' },
        { label: 'Proxy refusals', value: data.proxyRefusals, note: `last ${data.windowDays} days` },
        { label: 'Registry refusals', value: data.registryRefusals, note: `last ${data.windowDays} days` },
        { label: 'Suspended accounts', value: data.suspended, note: `last ${data.windowDays} days` },
      ]}
      />
      <Panel title={`Refused deploys per day, last ${data.windowDays} days`} hint="Hover a bar for refusals and distinct users.">
        <BarChart
          label="Refused deploys per day" labels={data.perDay.map((day) => shortDay(day.day))} series={[data.perDay.map((day) => day.total)]}
          seriesClasses={['fill-color-error']} every={3}
          tip={(i) => `${data.perDay[i].total} refusals, ${users[i]} users`}
        />
      </Panel>
      <Panel title="Latest refusals" hint="Nothing listed here ever ran."><Latest rows={data.latest} /></Panel>
    </div>
  );
}

export function AbuseAnalyticsTab() {
  const query = useAbuseAnalytics(true);
  return (
    <div className="space-y-4">
      <Lede title="Abuse">Deploys the platform refused because they looked like a proxy or came from a refused registry.</Lede>
      <Loaded query={query}>{(data) => <Body data={data} />}</Loaded>
    </div>
  );
}
