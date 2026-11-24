import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Plus, SlidersHorizontal } from 'lucide-react';
import { useTiers } from '../../api/tiers';
import { orgUsage } from '../usage/usageModel';
import { useAccessTokens } from '../../api/accessTokens';
import { useAppHostingEnabled, useFeatureEnabled, useSitesEnabled } from '../../hooks/useDeploymentMode';
import { useCurrentOrg, useMyProjects } from '../../hooks/useMemberOrgs';
import { useDocumentTitle } from '../../hooks/useDocumentTitle';
import { AttentionBar } from './AttentionBar';
import { BuildWidget, CreditsWidget, HealthWidget, RecentlyVisitedWidget, SecurityWidget, type BuildLink } from './HomeWidgets';
import { attentionItems, healthCounts, securitySummary, type AppInfo } from './model';
import { describeVisit, loadVisits, type Visit } from './recentlyVisited';
import { useOpenDatabases, useProjectServices } from './useHomeData';
import { useHiddenWidgets } from './widgetVisibility';
import type { DatabaseInstance } from '../../types';

const VISIBLE_VISITS = 5;

const WIDGETS = [
  { id: 'recent', title: 'Recently visited' },
  { id: 'health', title: 'Health' },
  { id: 'credits', title: 'Credits & usage' },
  { id: 'security', title: 'Security' },
  { id: 'build', title: 'Build something' },
] as const;

const BUTTON = 'inline-flex items-center gap-1.5 rounded-lg px-3 min-h-9 text-sm font-medium';

function recentVisits(projects: readonly DatabaseInstance[] | undefined): Visit[] {
  return loadVisits()
    .map((path) => describeVisit(path, projects))
    .filter((visit): visit is Visit => visit !== null)
    .slice(0, VISIBLE_VISITS);
}

// `ready` is false while the projects load or failed to load: with no answer there is no "no projects" to advise on.
function useBuildLinks(projects: readonly DatabaseInstance[], ready: boolean): { links: BuildLink[]; hint?: string } {
  const apps = useAppHostingEnabled().enabled;
  const sites = useSitesEnabled().enabled;
  const mcp = useFeatureEnabled('mcp').enabled;
  const target = projects.find((p) => p.status === 'ACTIVE');
  const links: BuildLink[] = [{ to: '/provision', label: 'Create a Postgres database' }];
  if (!ready) return { links };
  if (!target) return { links, hint: 'Create a project first, then add apps, sites and AI tools to it.' };
  const base = `/project/${target.projectId}`;
  if (apps) links.push({ to: `${base}/containers/new`, label: 'Deploy an app from a container image' });
  if (sites) links.push({ to: `${base}/sites/new`, label: 'Host a static site' });
  if (mcp) links.push({ to: `${base}/ai-tools`, label: 'Connect an AI coding tool (MCP)' });
  return { links };
}

function CustomizePanel({ hidden, onToggle }: { readonly hidden: readonly string[]; readonly onToggle: (id: string) => void }) {
  return (
    <fieldset className="flex flex-wrap gap-x-5 gap-y-2 rounded-xl border border-border-primary bg-surface-card p-4 text-sm">
      <legend className="px-1 text-text-secondary">Show on Home</legend>
      {WIDGETS.map((widget) => (
        <label key={widget.id} className="flex items-center gap-2 text-text-primary">
          <input type="checkbox" checked={!hidden.includes(widget.id)} onChange={() => onToggle(widget.id)} /> {widget.title}
        </label>
      ))}
    </fieldset>
  );
}

// The Home page of the console layout (studio_v2). Every figure comes from an API the Studio already uses;
// a widget with nothing to read is left out or says so, never filled with a guess.
export function HomePage() {
  useDocumentTitle('Home');
  const { org } = useCurrentOrg();
  const { data, isLoading, error } = useMyProjects();
  const projects = org ? data.filter((p) => p.orgId === org.id) : data;
  const tiers = useTiers();
  const plan = org ? orgUsage(projects, tiers.data?.find((t) => t.tier === org.tier), new Map()) : undefined;
  const tokens = useAccessTokens();
  const { byProject } = useProjectServices(projects);
  const openDatabases = useOpenDatabases(projects);
  const projectsReady = !isLoading && !error;
  const build = useBuildLinks(projects, projectsReady);
  const { hidden, toggle } = useHiddenWidgets();
  const [customizing, setCustomizing] = useState(false);
  const now = new Date();

  const apps: AppInfo[] = [...byProject.entries()].flatMap(([projectId, loaded]) =>
    (loaded.apps ?? []).map((app) => ({ projectId, appId: app.id, name: app.name, status: app.status })));
  const appsKnown = projects.length > 0 && projects.every((p) => byProject.get(p.projectId)?.apps !== undefined);
  const tokenList = tokens.data ?? [];
  const attention = attentionItems({ projects, tokens: tokenList, apps, now });
  const shown = (id: string) => !hidden.includes(id);

  return (
    <div className="mx-auto max-w-7xl space-y-5" data-testid="home-page">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold text-text-primary">Home</h1>
        {org && <span className="text-sm text-text-secondary">{org.name}</span>}
        <div className="ml-auto flex gap-2">
          <button type="button" aria-expanded={customizing} onClick={() => setCustomizing((v) => !v)}
            className={`${BUTTON} border border-border-primary text-text-primary hover:bg-surface-hover`}>
            <SlidersHorizontal className="h-4 w-4" aria-hidden="true" /> Customize widgets
          </button>
          <Link to="/provision" className={`${BUTTON} bg-purple-600 text-white hover:bg-purple-500`}>
            <Plus className="h-4 w-4" aria-hidden="true" /> New project
          </Link>
        </div>
      </div>

      {customizing && <CustomizePanel hidden={hidden} onToggle={toggle} />}
      <AttentionBar items={attention} />

      {Boolean(error) && (
        <p role="alert" className="rounded-xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-400">
          Could not load your projects, so Health, Security and the alerts above may be incomplete.
        </p>
      )}

      <div className="grid gap-4" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 300px), 1fr))' }}>
        {shown('recent') && projectsReady && <RecentlyVisitedWidget visits={recentVisits(projects)} />}
        {shown('health') && !error && !isLoading && (
          <HealthWidget counts={healthCounts(projects)} failingApps={appsKnown ? apps.filter((a) => a.status === 'FAILED').length : null} />
        )}
        {shown('credits') && projectsReady && <CreditsWidget planName={org?.tier} projects={plan?.projects} limitsKnown={plan?.limitsKnown ?? false} />}
        {shown('security') && (
          <SecurityWidget
            summary={{ ...securitySummary({ tokens: tokenList, openDatabases: 0, now }), openDatabases: openDatabases.open }}
            openLoading={!projectsReady || openDatabases.isLoading}
            openUnchecked={openDatabases.unchecked}
            keysKnown={!tokens.isError && !tokens.isLoading}
          />
        )}
        {shown('build') && <BuildWidget links={build.links} hint={build.hint} />}
      </div>
    </div>
  );
}
