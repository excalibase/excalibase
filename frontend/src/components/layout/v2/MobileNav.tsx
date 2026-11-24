import { useEffect, useRef, useSyncExternalStore, type KeyboardEvent, type ReactNode, type RefObject } from 'react';
import { Menu, X } from 'lucide-react';

export const PHONE_QUERY = '(max-width: 768px)';

function subscribe(onChange: () => void) {
  const query = globalThis.matchMedia(PHONE_QUERY);
  query.addEventListener('change', onChange);
  return () => query.removeEventListener('change', onChange);
}

// True while the window is 768px wide or narrower.
export function useIsPhone() {
  return useSyncExternalStore(subscribe, () => globalThis.matchMedia(PHONE_QUERY).matches, () => false);
}

const FOCUSABLE = 'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])';

interface MobileNavBarProps {
  readonly open: boolean;
  readonly label: string;
  readonly onToggle: () => void;
  readonly buttonRef: RefObject<HTMLButtonElement>;
}

// The phone's replacement for the rail: one row with the menu button and the current section.
export function MobileNavBar({ open, label, onToggle, buttonRef }: MobileNavBarProps) {
  return (
    <div className="flex items-center gap-3 border-b border-border-primary bg-surface-card px-4 py-1 flex-shrink-0">
      <button ref={buttonRef} type="button" aria-label="Menu" aria-expanded={open} aria-controls="project-nav-drawer" onClick={onToggle}
        className="inline-flex min-h-10 min-w-10 items-center justify-center rounded-lg text-text-secondary hover:bg-surface-hover hover:text-text-primary">
        <Menu className="w-5 h-5" aria-hidden="true" />
      </button>
      <span className="truncate text-sm font-medium text-text-primary">{label}</span>
    </div>
  );
}

interface DrawerProps {
  readonly onClose: () => void;
  readonly children: ReactNode;
}

// A modal drawer: focus moves in, Tab stays inside, Escape and the backdrop close it.
export function NavDrawer({ onClose, children }: DrawerProps) {
  const panel = useRef<HTMLDivElement>(null);
  useEffect(() => { panel.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus(); }, []);

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'Escape') { e.stopPropagation(); onClose(); return; }
    if (e.key !== 'Tab' || !panel.current) return;
    const items = [...panel.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
    const first = items[0];
    const last = items.at(-1);
    if (!first || !last) return;
    if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
    else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
  };

  return (
    <div className="fixed inset-0 z-40" onKeyDown={onKeyDown}>
      <button type="button" aria-label="Close menu" tabIndex={-1} onClick={onClose} className="absolute inset-0 bg-black/50" />
      <div ref={panel} id="project-nav-drawer" role="dialog" aria-modal="true" aria-label="Project navigation"
        className="relative flex h-full w-[85vw] max-w-xs flex-col overflow-y-auto bg-surface-card shadow-xl">
        <button type="button" aria-label="Close navigation" onClick={onClose}
          className="ml-auto mr-2 mt-2 inline-flex min-h-10 min-w-10 flex-shrink-0 items-center justify-center rounded-lg text-text-secondary hover:bg-surface-hover">
          <X className="w-5 h-5" aria-hidden="true" />
        </button>
        {children}
      </div>
    </div>
  );
}
