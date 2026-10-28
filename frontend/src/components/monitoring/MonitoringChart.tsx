import {
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import type { MonitoringRange } from '../../api/monitoring';
import { timeTick, tooltipValue } from '../../utils/hostMetrics';

export interface ChartLine {
  readonly key: string;
  readonly name: string;
  readonly color: string;
}

const tooltipStyle = {
  contentStyle: {
    backgroundColor: 'var(--surface-card)',
    border: '1px solid var(--border-primary)',
    borderRadius: '8px',
  },
  labelStyle: { color: 'var(--text-primary)' },
  itemStyle: { color: 'var(--text-secondary)' },
};

interface MonitoringChartProps {
  readonly title: string;
  readonly data: readonly object[];
  readonly lines: readonly ChartLine[];
  readonly range: MonitoringRange;
  readonly format: (value: number) => string;
  readonly height?: number;
}

// connectNulls is off: an unmeasured point is a gap, not a zero.
export function MonitoringChart({
  title,
  data,
  lines,
  range,
  format,
  height = 220,
}: MonitoringChartProps) {
  const tick = timeTick(range);
  return (
    <div className="bg-surface-card border border-border-primary rounded-xl p-5">
      <h3 className="text-sm font-semibold text-text-primary mb-3">{title}</h3>
      <ResponsiveContainer width="100%" height={height}>
        <LineChart data={[...data]}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border-primary)" />
          <XAxis
            dataKey="timestamp"
            tickFormatter={tick}
            stroke="var(--text-tertiary)"
            tick={{ fontSize: 11 }}
            minTickGap={24}
          />
          <YAxis
            stroke="var(--text-tertiary)"
            tick={{ fontSize: 11 }}
            tickFormatter={format}
            width={72}
          />
          <Tooltip labelFormatter={tick} formatter={tooltipValue(format)} {...tooltipStyle} />
          <Legend />
          {lines.map((line) => (
            <Line
              key={line.key}
              type="monotone"
              dataKey={line.key}
              name={line.name}
              stroke={line.color}
              dot={false}
              connectNulls={false}
              strokeWidth={2}
              isAnimationActive={false}
            />
          ))}
        </LineChart>
      </ResponsiveContainer>
    </div>
  );
}
