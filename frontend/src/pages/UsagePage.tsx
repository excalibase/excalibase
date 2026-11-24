import { Link } from 'react-router-dom';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { planLabel } from '../hooks/useMemberOrgs';
import { useOrgUsage } from './usage/useOrgUsage';
import { appsValue, gib, Line, projectsValue, Section } from './usage/UsageParts';

const CELL = 'px-3 py-2 text-left';

// Plan and use for the current org, read from the project and plan lists. CPU and memory are plan sizes, not live readings.
export function UsagePage() {
  useDocumentTitle('Usage & credits');
  const { orgName, tierName, tier, usage, isLoading, failed } = useOrgUsage();
  return (
    <div className="mx-auto max-w-3xl space-y-4" data-testid="usage-page">
      <h1 className="text-xl font-semibold text-text-primary">Usage &amp; credits</h1>
      {failed && <p role="alert" className="rounded-xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-400">Could not load your usage. Reload the page to try again.</p>}
      {isLoading && <p className="text-sm text-text-secondary">Loading usage…</p>}
      {!failed && !isLoading && !tierName && <p className="text-sm text-text-secondary">You are not in an organization yet. <Link to="/instances" className="text-purple-400 hover:underline">See your projects</Link></p>}
      {tierName && (
        <Section title="Plan">
          <Line label={orgName ?? 'Organization'} value={planLabel(tierName)} />
          {tier && <Line label="Database size" value={`${tier.cpu} CPU, ${tier.memory} memory per database`} />}
          {tier && <Line label="Disk per project" value={`${tier.storageSize}, up to ${tier.maxStorageSize}`} />}
        </Section>
      )}
      {usage && tierName && (
        <Section title="Usage">
          <Line label="Projects" value={projectsValue(usage.projects, usage.limitsKnown)} />
          <Line label="Databases" value={usage.databases} />
          {usage.storageGiB !== null && <Line label="Disk allocated" value={usage.storagePartial ? `≥ ${gib(usage.storageGiB)} (some unknown)` : gib(usage.storageGiB)} />}
          {usage.perProject.length > 0 && (
            <table className="w-full text-text-primary">
              <thead className="text-text-secondary"><tr><th className={CELL}>Project</th><th className={CELL}>Apps</th><th className={CELL}>Disk</th></tr></thead>
              <tbody>
                {usage.perProject.map((p) => (
                  <tr key={p.projectId} className="border-t border-border-primary">
                    <td className={CELL}><Link to={`/project/${p.projectId}`} className="hover:underline">{p.name}</Link></td>
                    <td className={CELL}>{appsValue(p.apps, tier)}</td>
                    <td className={CELL}>{p.storageGiB === null ? '—' : gib(p.storageGiB)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="text-text-secondary">Live CPU and memory readings are on each project's Usage tab.</p>
        </Section>
      )}
    </div>
  );
}
