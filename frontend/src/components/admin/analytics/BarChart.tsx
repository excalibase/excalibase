import { useState } from 'react';
import { cn } from '../../../utils/cn';

const WIDTH = 760;
const HEIGHT = 220;
const LEFT = 34;
const RIGHT = 6;
const TOP = 18;
const BOTTOM = 26;

export interface ChartScale {
  top: number;
  step: number;
}

// The axis top is the largest total rounded up to a whole step; an empty chart keeps one step of height.
export function chartScale(max: number): ChartScale {
  let step = 5;
  if (max > 50) step = 20;
  else if (max > 20) step = 10;
  return { top: Math.ceil(max / step) * step || step, step };
}

// The tooltip is centred on its bar, except at the edges where it would be cut off by the chart's box.
export function tooltipShift(index: number, count: number): string {
  if (index >= count * 0.85) return '-translate-x-full';
  if (index < count * 0.15) return 'translate-x-0';
  return '-translate-x-1/2';
}

interface BarChartProps {
  readonly label: string;
  readonly labels: readonly string[];
  // One array per stacked series, a value per label.
  readonly series: readonly (readonly number[])[];
  // Tailwind fill class of each series.
  readonly seriesClasses: readonly string[];
  readonly tip: (index: number) => string;
  // Show every nth axis label so thirty days stay readable.
  readonly every?: number;
}

// Stacked bars drawn as SVG: the numbers a chart needs here are a handful of
// days, so a charting library would only add weight. Hover (or focus) a day for its figures.
export function BarChart({ label, labels, series, seriesClasses, tip, every = 1 }: BarChartProps) {
  const [hovered, setHovered] = useState<number | null>(null);
  const totals = labels.map((_, i) => series.reduce((sum, values) => sum + (values[i] ?? 0), 0));
  const { top, step } = chartScale(Math.max(0, ...totals));
  const plotHeight = HEIGHT - TOP - BOTTOM;
  const y = (value: number) => TOP + plotHeight * (1 - value / top);
  const slot = (WIDTH - LEFT - RIGHT) / labels.length;
  const ticks = Array.from({ length: top / step + 1 }, (_, i) => i * step);
  return (
    <div className="relative overflow-x-auto">
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={label} className="block w-full h-auto min-w-[520px]">
        {ticks.map((tick) => (
          <g key={tick}>
            <line x1={LEFT} x2={WIDTH - RIGHT} y1={y(tick)} y2={y(tick)} className="stroke-border-primary" strokeWidth={1} strokeDasharray={tick ? '2 3' : undefined} />
            <text x={LEFT - 6} y={y(tick) + 4} textAnchor="end" fontSize={10} className="fill-text-tertiary">{tick}</text>
          </g>
        ))}
        {labels.map((name, i) => {
          let stacked = 0;
          return (
            <g key={name}>
              {series.map((values, s) => {
                const value = values[i] ?? 0;
                const base = stacked;
                stacked += value;
                if (value <= 0) return null;
                return <rect key={seriesClasses[s]} data-bar x={LEFT + i * slot + 2} y={y(base + value)} width={Math.max(slot - 4, 2)} height={y(base) - y(base + value)} rx={2} className={seriesClasses[s]} opacity={hovered === i ? 0.8 : 1} />;
              })}
              {i % every === 0 && <text x={LEFT + i * slot + slot / 2} y={HEIGHT - 8} textAnchor="middle" fontSize={10} className="fill-text-tertiary">{name}</text>}
              <rect
                data-testid={`bar-hit-${i}`} x={LEFT + i * slot} y={TOP} width={slot} height={plotHeight} fill="transparent" tabIndex={0}
                aria-label={`${name} ${tip(i)}`}
                onMouseEnter={() => setHovered(i)} onMouseLeave={() => setHovered(null)}
                onFocus={() => setHovered(i)} onBlur={() => setHovered(null)}
              />
            </g>
          );
        })}
      </svg>
      {hovered !== null && (
        <div
          role="tooltip"
          className={cn('absolute pointer-events-none -translate-y-full -mt-1.5 whitespace-nowrap rounded bg-text-primary px-2 py-1 text-xs text-bg-primary', tooltipShift(hovered, labels.length))}
          style={{ left: `${((LEFT + hovered * slot + slot / 2) / WIDTH) * 100}%`, top: `${(y(totals[hovered]) / HEIGHT) * 100}%` }}
        >
          {`${labels[hovered]} ${tip(hovered)}`}
        </div>
      )}
    </div>
  );
}
