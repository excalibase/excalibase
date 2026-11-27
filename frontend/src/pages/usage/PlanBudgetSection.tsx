import { cpuText, memoryText, useOrgPlanBudget, type BudgetService, type BudgetSize } from '../../api/planBudget';
import { Section } from './UsageParts';

interface Dimension {
  readonly label: string;
  readonly used: number;
  readonly limit: number;
  readonly text: (value: number) => string;
}

function dimensions(used: BudgetSize, budget: BudgetSize): Dimension[] {
  return [
    { label: 'CPU', used: used.cpuMilli, limit: budget.cpuMilli, text: cpuText },
    { label: 'Memory', used: used.memoryBytes, limit: budget.memoryBytes, text: memoryText },
    { label: 'Disk', used: used.diskBytes, limit: budget.diskBytes, text: memoryText },
  ];
}

const percent = (used: number, limit: number) => (limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0);

function Meter({ dimension }: { readonly dimension: Dimension }) {
  const share = percent(dimension.used, dimension.limit);
  return (
    <div className="space-y-1">
      <div className="flex justify-between gap-3 text-text-primary">
        <span>{dimension.label}</span>
        <span className="font-medium">{`${dimension.text(dimension.used)} of ${dimension.text(dimension.limit)}`}</span>
      </div>
      <div role="meter" aria-label={dimension.label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={share} className="h-2 overflow-hidden rounded bg-surface-secondary">
        <div className={share >= 100 ? 'h-full bg-red-500' : 'h-full bg-purple-500'} style={{ width: `${share}%` }} />
      </div>
    </div>
  );
}

const KIND_LABEL: Record<BudgetService['kind'], string> = { database: 'Database', app: 'App', functions: 'Functions' };

function ServiceTable({ services }: { readonly services: readonly BudgetService[] }) {
  if (services.length === 0) return null;
  return (
    <table className="w-full text-text-primary">
      <thead className="text-text-secondary"><tr><th className="px-3 py-2 text-left">Service</th><th className="px-3 py-2 text-left">Kind</th><th className="px-3 py-2 text-left">CPU</th><th className="px-3 py-2 text-left">Memory</th></tr></thead>
      <tbody>
        {services.map((service) => (
          <tr key={`${service.kind}-${service.projectId}-${service.name}`} className="border-t border-border-primary">
            <td className="px-3 py-2">{service.name}</td>
            <td className="px-3 py-2">{KIND_LABEL[service.kind]}</td>
            <td className="px-3 py-2">{service.unrecorded ? 'unknown' : cpuText(service.size.cpuMilli)}</td>
            <td className="px-3 py-2">{service.unrecorded ? 'unknown' : memoryText(service.size.memoryBytes)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// The plan's CPU, memory, disk and service count, used against the limit, per service. Absent while the feature is off.
export function PlanBudgetSection({ orgId }: { readonly orgId: string | undefined }) {
  const { data, isError } = useOrgPlanBudget(orgId);
  if (isError) return <p role="alert" className="rounded-xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-400">Could not read your plan budget.</p>;
  if (!data) return null;
  const over = data.left.cpuMilli < 0 || data.left.memoryBytes < 0 || data.left.diskBytes < 0 || data.left.services < 0;
  return (
    <Section title="Plan budget">
      {over && <output className="block text-amber-400">You are over your plan. What you run keeps running; nothing new can be added until you free some.</output>}
      {dimensions(data.used, data.budget).map((dimension) => <Meter key={dimension.label} dimension={dimension} />)}
      <div className="flex justify-between gap-3 text-text-primary"><span>Services</span><span className="font-medium">{`${data.used.services} of ${data.budget.services}`}</span></div>
      <ServiceTable services={data.services} />
    </Section>
  );
}
