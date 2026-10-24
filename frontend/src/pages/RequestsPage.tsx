import { useState } from 'react';
import { Loader2 } from 'lucide-react';
import {
  AreaChart, Area, LineChart, Line, XAxis, YAxis, CartesianGrid, Tooltip, ResponsiveContainer, Legend,
} from 'recharts';
import { useRouteProjectId } from '../hooks/useRouteProjectId';
import { useRequestCharts } from '../hooks/useRequestCharts';
import { cn } from '../utils/cn';
import {
  REQUEST_RANGES, REQUEST_FAMILIES, FAMILY_LABELS,
  requestSeries, errorRateSeries, latencySeries, realtimeSeries, familySummaries,
  formatLatency, formatRate, stepLabel,
  type RequestCharts, type RequestFamily, type RequestRange,
} from '../utils/requestCharts';

const FAMILY_COLORS: Record<RequestFamily, string> = { graphql: '#e535ab', rest: '#3b82f6', auth: '#10b981' };

const tooltipStyle = {
  contentStyle: { backgroundColor: 'var(--surface-card)', border: '1px solid var(--border-primary)', borderRadius: '8px' },
  labelStyle: { color: 'var(--text-primary)' },
  itemStyle: { color: 'var(--text-secondary)' },
};

function ChartCard({ title, children }: { readonly title: string; readonly children: React.ReactNode }) {
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-6">
      <h3 className="text-sm font-semibold text-text-primary mb-4">{title}</h3>
      <ResponsiveContainer width="100%" height={240}>{children}</ResponsiveContainer>
    </div>
  );
}

function Segmented<T extends string>({ options, value, onChange, label }: {
  readonly options: readonly T[];
  readonly value: T;
  readonly onChange: (next: T) => void;
  readonly label: (option: T) => string;
}) {
  return (
    <div className="inline-flex rounded-lg border border-border-primary overflow-hidden">
      {options.map((option) => (
        <button
          key={option}
          type="button"
          onClick={() => onChange(option)}
          className={cn('px-3 py-1.5 text-xs font-medium', option === value ? 'bg-accent-primary/20 text-text-primary' : 'text-text-secondary hover:bg-surface-hover')}
        >
          {label(option)}
        </button>
      ))}
    </div>
  );
}

function Summary({ charts }: { readonly charts: RequestCharts }) {
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="text-text-tertiary border-b border-border-primary text-left">
            <th className="px-6 py-3 font-medium">Surface</th>
            <th className="px-6 py-3 font-medium">Requests</th>
            <th className="px-6 py-3 font-medium">Error rate</th>
            <th className="px-6 py-3 font-medium">p50</th>
            <th className="px-6 py-3 font-medium">p95</th>
          </tr>
        </thead>
        <tbody>
          {familySummaries(charts).map((summary) => (
            <tr key={summary.family} className="border-b border-border-primary last:border-0">
              <td className="px-6 py-3 text-text-primary">{FAMILY_LABELS[summary.family]}</td>
              {summary.family === 'realtime' ? (
                <td className="px-6 py-3 text-text-secondary" colSpan={4}>
                  peak {summary.connections.toLocaleString()} open connections, {summary.messages.toLocaleString()} messages delivered
                </td>
              ) : (
                <>
                  <td className="px-6 py-3 text-text-primary">{summary.requests.toLocaleString()}</td>
                  <td className="px-6 py-3 text-text-primary">{formatRate(summary.errorRate)}</td>
                  <td className="px-6 py-3 text-text-primary">{formatLatency(summary.p50Ms)}</td>
                  <td className="px-6 py-3 text-text-primary">{formatLatency(summary.p95Ms)}</td>
                </>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function Charts({ charts, range }: { readonly charts: RequestCharts; readonly range: RequestRange }) {
  const [family, setFamily] = useState<RequestFamily>('graphql');
  const tick = (timestamp: string) => stepLabel(timestamp, range);
  const axes = (unit?: string) => (
    <>
      <CartesianGrid strokeDasharray="3 3" stroke="var(--border-primary)" />
      <XAxis dataKey="timestamp" tickFormatter={tick} stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} minTickGap={24} />
      <YAxis stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} unit={unit} />
      <Tooltip labelFormatter={(label) => tick(String(label))} {...tooltipStyle} />
      <Legend />
    </>
  );
  return (
    <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
      <ChartCard title="Requests">
        <AreaChart data={requestSeries(charts)}>
          {axes()}
          {REQUEST_FAMILIES.map((name) => (
            <Area key={name} type="monotone" dataKey={name} name={FAMILY_LABELS[name]} stackId="requests"
              stroke={FAMILY_COLORS[name]} fill={FAMILY_COLORS[name]} fillOpacity={0.25} strokeWidth={2} />
          ))}
        </AreaChart>
      </ChartCard>
      <ChartCard title="Error rate">
        <LineChart data={errorRateSeries(charts)}>
          {axes('%')}
          <Line type="monotone" dataKey="clientErrors" name="4xx and GraphQL errors" stroke="#f59e0b" dot={false} connectNulls={false} strokeWidth={2} />
          <Line type="monotone" dataKey="serverErrors" name="5xx" stroke="#ef4444" dot={false} connectNulls={false} strokeWidth={2} />
        </LineChart>
      </ChartCard>
      <div className="bg-surface-card border border-border-primary rounded-xl p-6">
        <div className="flex items-center justify-between mb-4">
          <h3 className="text-sm font-semibold text-text-primary">Latency</h3>
          <Segmented options={REQUEST_FAMILIES} value={family} onChange={setFamily} label={(name) => FAMILY_LABELS[name]} />
        </div>
        <ResponsiveContainer width="100%" height={240}>
          <LineChart data={latencySeries(charts, family)}>
            {axes(' ms')}
            <Line type="monotone" dataKey="p50" name="p50" stroke="#3b82f6" dot={false} connectNulls={false} strokeWidth={2} />
            <Line type="monotone" dataKey="p95" name="p95" stroke="#a855f7" dot={false} connectNulls={false} strokeWidth={2} />
          </LineChart>
        </ResponsiveContainer>
      </div>
      <ChartCard title="Realtime">
        <LineChart data={realtimeSeries(charts)}>
          {axes()}
          <Line type="monotone" dataKey="connections" name="Open connections" stroke="#10b981" dot={false} strokeWidth={2} />
          <Line type="monotone" dataKey="messages" name="Messages delivered" stroke="#6366f1" dot={false} strokeWidth={2} />
        </LineChart>
      </ChartCard>
    </div>
  );
}

export function RequestsPage() {
  const projectId = useRouteProjectId();
  const [range, setRange] = useState<RequestRange>('1h');
  const { data, isLoading, isError } = useRequestCharts(projectId, range);

  return (
    <div className="max-w-7xl mx-auto space-y-6">
      <div className="flex items-center justify-between gap-4 flex-wrap">
        <div>
          <h2 className="text-lg font-semibold text-text-primary">Requests</h2>
          <p className="text-sm text-text-secondary">GraphQL, REST, auth and realtime traffic of this project, sampled every minute.</p>
        </div>
        <Segmented options={REQUEST_RANGES} value={range} onChange={setRange} label={(option) => option} />
      </div>
      {isLoading && (
        <div className="bg-surface-card border border-border-primary rounded-xl flex items-center justify-center gap-3 py-16">
          <Loader2 className="w-5 h-5 animate-spin text-purple-400" />
          <span className="text-sm text-text-secondary">Fetching request metrics...</span>
        </div>
      )}
      {isError && !data && <div className="text-center py-20 text-text-secondary">Request metrics could not be loaded.</div>}
      {data && (
        <>
          <Summary charts={data} />
          <Charts charts={data} range={range} />
        </>
      )}
    </div>
  );
}
