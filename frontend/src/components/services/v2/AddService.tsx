import { useEffect, useRef, useState } from 'react';
import { Link } from 'react-router-dom';
import { Box, Database, FolderOpen, Globe, type LucideIcon } from 'lucide-react';
import { secondaryButton } from '../../containers/ContainerBits';
import type { ProjectServices } from './useProjectServices';

interface Option {
  readonly label: string;
  readonly detail: string;
  readonly to: string;
  readonly icon: LucideIcon;
}

// The create flows that exist today; a service the install or the caller
// cannot create is not offered.
export function addServiceOptions(projectId: string, services: ProjectServices): Option[] {
  const base = `/project/${projectId}`;
  const options: Option[] = [];
  if (services.project?.noDatabase && services.project.canAddDatabase) {
    options.push({ label: 'Database', detail: 'Managed PostgreSQL with a GraphQL and REST API', to: `${base}/database/add`, icon: Database });
  }
  if (services.appHosting) options.push({ label: 'App', detail: 'Run a container image on a public URL', to: `${base}/containers/new`, icon: Box });
  if (services.sitesOn) options.push({ label: 'Site', detail: 'Host a built frontend', to: `${base}/sites/new`, icon: Globe });
  options.push({ label: 'Storage', detail: 'Buckets for files', to: `${base}/storage`, icon: FolderOpen });
  return options;
}

export function AddServiceMenu({ projectId, services }: { readonly projectId: string; readonly services: ProjectServices }) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const close = (event: MouseEvent | KeyboardEvent) => {
      if (event instanceof KeyboardEvent ? event.key === 'Escape' : !root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', close);
    return () => {
      document.removeEventListener('mousedown', close);
      document.removeEventListener('keydown', close);
    };
  }, [open]);

  return (
    <div ref={root} className="relative">
      <button type="button" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)} className={secondaryButton}>
        + Add service
      </button>
      {open && (
        <ul role="menu" aria-label="Add service" className="absolute right-0 z-20 mt-1 w-72 rounded-xl border border-border-primary bg-surface-card p-1 shadow-lg">
          {addServiceOptions(projectId, services).map(({ label, detail, to, icon: Icon }) => (
            <li key={label} role="none">
              <Link role="menuitem" to={to} className="flex items-start gap-3 rounded-lg px-3 py-2 hover:bg-surface-hover">
                <Icon className="mt-0.5 h-4 w-4 text-purple-400" aria-hidden="true" />
                <span>
                  <span className="block text-sm text-text-primary">{label}</span>
                  <span className="block text-xs text-text-tertiary">{detail}</span>
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// A project with nothing in it yet.
export function EmptyServices({ projectId, services }: { readonly projectId: string; readonly services: ProjectServices }) {
  return (
    <section data-testid="services-empty" className="mx-auto max-w-2xl space-y-5 py-10 text-center">
      <h2 className="text-lg font-semibold text-text-primary">Add your first service</h2>
      <p className="text-sm text-text-secondary">
        A project holds the services that belong together: a database, apps, sites and storage. Start with whichever you need now.
      </p>
      <ul className="grid gap-3 text-left sm:grid-cols-2">
        {addServiceOptions(projectId, services).map(({ label, detail, to, icon: Icon }) => (
          <li key={label}>
            <Link to={to} className="flex h-full items-start gap-3 rounded-xl border border-border-primary bg-surface-card p-4 hover:bg-surface-hover">
              <Icon className="mt-0.5 h-5 w-5 text-purple-400" aria-hidden="true" />
              <span>
                <span className="block text-sm font-semibold text-text-primary">{label}</span>
                <span className="block text-xs text-text-tertiary">{detail}</span>
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}
