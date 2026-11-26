import { useEffect, useMemo } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Command } from 'cmdk';
import { BookOpen, Database, Compass } from 'lucide-react';
import { useMyProjects, useCurrentOrg } from '../../../hooks/useMemberOrgs';
import { sectionLabel, visibleNavItems, visibleProjectNav, type NavCapabilities } from '../navigation';
import { useAppHostingEnabled, useFeatureEnabled, useFeatures, useSitesEnabled } from '../../../hooks/useDeploymentMode';
import { useInstance } from '../../../hooks/useProvisioning';
import { useProjectIsDocumentDB } from '../../../hooks/useDocuments';
import { isMySQLProject } from '../../../utils/engine';
import { useAuthStore } from '../../../stores/auth-store';
import type { Feature } from '../../../hooks/useDeploymentMode';
import { DOCS_LINKS, shellNav } from './navV2';

const ITEM = 'flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-secondary cursor-pointer data-[selected=true]:bg-purple-500/10 data-[selected=true]:text-purple-400';
const GROUP = '[&_[cmdk-group-heading]]:px-3 [&_[cmdk-group-heading]]:py-1.5 [&_[cmdk-group-heading]]:text-[10px] [&_[cmdk-group-heading]]:uppercase [&_[cmdk-group-heading]]:tracking-wider [&_[cmdk-group-heading]]:text-text-tertiary';

interface Destination { key: string; label: string; to: string; external?: boolean }

// Pages of the project that is open: the leaf pages the project navigation itself shows, by the same rules.
export function projectPages(
  projectId: string | undefined,
  capabilities: NavCapabilities,
  documentDb: boolean,
  features: Partial<Record<Feature, boolean>>,
): Destination[] {
  if (!projectId) return [];
  const postgres = capabilities.postgres !== false;
  return visibleProjectNav(capabilities).flatMap((section) => {
    const pages = section.children
      ? visibleNavItems(section.children, documentDb, postgres, features)
      : [{ label: sectionLabel(section, true), to: section.to ?? '' }];
    return pages.map((page) => ({
      key: `${section.key}:${page.to}`,
      label: section.children ? `${sectionLabel(section, true)}: ${page.label}` : page.label,
      to: `/project/${projectId}/${page.to}`.replace(/\/$/, ''),
    }));
  });
}

// ⌘K / Ctrl+K: searches the person's own projects and where they can go.
export function CommandPalette({ open, onOpenChange }: { readonly open: boolean; readonly onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const { projectId } = useParams<{ projectId: string }>();
  const { data: projects } = useMyProjects();
  const { org } = useCurrentOrg();
  const { enabled: appHosting } = useAppHostingEnabled();
  const { enabled: mcp } = useFeatureEnabled('mcp');
  const { enabled: sites } = useSitesEnabled();
  const { data: project } = useInstance(projectId ?? '');
  const { data: documentDb = false } = useProjectIsDocumentDB(projectId ?? '');
  const features = useFeatures();
  const platformStaff = useAuthStore((s) => s.user?.role === 'platform_admin' || s.user?.role === 'platform_operator');
  const pages = useMemo(
    () => projectPages(projectId, { appHosting, mcp, sites, postgres: !isMySQLProject(project), platformStaff }, documentDb, features),
    [projectId, appHosting, mcp, sites, project, platformStaff, documentDb, features],
  );

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpenChange(true);
      } else if (e.key === 'Escape' && open) {
        onOpenChange(false);
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [open, onOpenChange]);

  const destinations = useMemo<Destination[]>(() => {
    const { main, help } = shellNav(org?.id);
    return [...main, ...help].map((i) => ({ key: i.key, label: i.label, to: i.to, external: i.external }));
  }, [org?.id]);

  const go = (to: string, external?: boolean) => {
    onOpenChange(false);
    if (external) window.open(to, '_blank', 'noopener');
    else navigate(to);
  };

  if (!open) return null;

  return (
    <>
      <button type="button" aria-label="Close search" tabIndex={-1} onClick={() => onOpenChange(false)} className="fixed inset-0 z-[60] cursor-default border-0 bg-black/50 p-0" />
      <dialog open aria-modal="true" aria-label="Search" className="m-0 p-0 text-text-primary fixed left-1/2 top-[18vh] z-[61] w-[calc(100%-2rem)] max-w-lg -translate-x-1/2 overflow-hidden rounded-xl border border-border-primary bg-surface-card shadow-2xl">
        <Command label="Search">
      <Command.Input autoFocus placeholder="Search projects, pages and settings" className="w-full border-b border-border-primary bg-transparent px-4 py-3 text-sm text-text-primary outline-none placeholder:text-text-tertiary" />
      <Command.List className="max-h-80 overflow-y-auto p-2">
        <Command.Empty className="py-6 text-center text-sm text-text-tertiary">No results.</Command.Empty>
        {projects.length > 0 && (
          <Command.Group heading="Projects" className={GROUP}>
            {projects.map((p) => (
              <Command.Item key={p.projectId} value={`${p.projectName ?? ''} ${p.projectId}`} onSelect={() => go(`/project/${p.projectId}`)} className={ITEM}>
                <Database className="w-4 h-4" aria-hidden="true" />
                <span>{p.projectName?.trim() || p.projectId}</span>
                {p.projectName && <span className="ml-auto font-mono text-xs text-text-tertiary">{p.projectId}</span>}
              </Command.Item>
            ))}
          </Command.Group>
        )}
        <Command.Group heading="Go to" className={GROUP}>
          {destinations.map((d) => (
            <Command.Item key={d.key} value={`go ${d.label}`} onSelect={() => go(d.to, d.external)} className={ITEM}>
              <Compass className="w-4 h-4" aria-hidden="true" /> {d.label}
            </Command.Item>
          ))}
        </Command.Group>
        <Command.Group heading="Docs" className={GROUP}>
          {DOCS_LINKS.map((d) => (
            <Command.Item key={d.key} value={`docs ${d.label}`} onSelect={() => go(d.to, true)} className={ITEM}>
              <BookOpen className="w-4 h-4" aria-hidden="true" /> {d.label}
            </Command.Item>
          ))}
        </Command.Group>
        {projectId && (
          <Command.Group heading="This project" className={GROUP}>
            {pages.map((d) => (
              <Command.Item key={d.key} value={`project ${d.label}`} onSelect={() => go(d.to)} className={ITEM}>
                <Compass className="w-4 h-4" aria-hidden="true" /> {d.label}
              </Command.Item>
            ))}
          </Command.Group>
        )}
      </Command.List>
        </Command>
      </dialog>
    </>
  );
}
