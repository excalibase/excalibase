import { Area, AreaChart, CartesianGrid, Legend, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';

export interface TrendPoint {
  readonly time: string;
  readonly cpu: number | null;
  readonly memory: number | null;
}

// CPU and memory as a share of their limits. An unmeasured point is a gap.
export function MetricsTrend({ data }: { readonly data: readonly TrendPoint[] }) {
  return (
    <ResponsiveContainer width="100%" height={220}>
      <AreaChart data={[...data]}>
        <defs>
          <linearGradient id="trendCpu" x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.5} />
            <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
          </linearGradient>
          <linearGradient id="trendMemory" x1="0" y1="0" x2="0" y2="1">
            <stop offset="5%" stopColor="#a855f7" stopOpacity={0.5} />
            <stop offset="95%" stopColor="#a855f7" stopOpacity={0} />
          </linearGradient>
        </defs>
        <CartesianGrid strokeDasharray="3 3" stroke="var(--border-primary)" />
        <XAxis dataKey="time" stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} minTickGap={32} />
        <YAxis stroke="var(--text-tertiary)" tick={{ fontSize: 11 }} unit="%" width={44} />
        <Tooltip
          contentStyle={{ backgroundColor: 'var(--surface-card)', border: '1px solid var(--border-primary)', borderRadius: '8px' }}
          labelStyle={{ color: 'var(--text-primary)' }}
          itemStyle={{ color: 'var(--text-secondary)' }}
        />
        <Legend />
        <Area type="monotone" dataKey="cpu" stroke="#3b82f6" fill="url(#trendCpu)" name="CPU %" strokeWidth={2} />
        <Area type="monotone" dataKey="memory" stroke="#a855f7" fill="url(#trendMemory)" name="Memory %" strokeWidth={2} />
      </AreaChart>
    </ResponsiveContainer>
  );
}
