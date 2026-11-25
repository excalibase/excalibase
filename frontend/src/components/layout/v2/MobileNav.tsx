import { useEffect, useRef, useSyncExternalStore, type ReactNode, type RefObject } from 'react';
import { Menu, X } from 'lucide-react';

export const PHONE_QUERY = '(max-width: 767.98px)';

function subscribe(onChange: () => void) {
  const query = globalThis.matchMedia(PHONE_QUERY);
  query.addEventListener('change', onChange);
  return () => query.removeEventListener('change', onChange);
}

// True while the window is 768px wide or narrower.
export function useIsPhone() {
  return useSyncExternalStore(subscribe, () => globalThis.matchMedia(PHONE_QUERY).matches, () => false);
}

export const DRAWER_ID = 'project-nav-drawer';
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
      <button ref={buttonRef} type="button" aria-label="Menu" aria-expanded={open} aria-controls={open ? DRAWER_ID : undefined} onClick={onToggle}
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

// Everything next to the drawer stops taking focus and clicks while it is open.
function useInertBehind(drawer: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const root = drawer.current?.parentElement;
    const behind = root ? [...root.children].filter((child) => !child.contains(drawer.current)) : [];
    for (const child of behind) child.setAttribute('inert', '');
    return () => { for (const child of behind) child.removeAttribute('inert'); };
  }, [drawer]);
}

// A modal drawer: focus moves in and stays in, Escape and the backdrop close it.
export function NavDrawer({ onClose, children }: DrawerProps) {
  const panel = useRef<HTMLDialogElement>(null);
  const frame = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useInertBehind(frame);
  useEffect(() => { panel.current?.querySelector<HTMLElement>(FOCUSABLE)?.focus(); }, []);

  useEffect(() => {
    const onKeyDown = (e: globalThis.KeyboardEvent) => {
      if (e.key === 'Escape') { close.current(); return; }
      if (e.key !== 'Tab' || !panel.current) return;
      const items = [...panel.current.querySelectorAll<HTMLElement>(FOCUSABLE)];
      const first = items[0];
      const last = items.at(-1);
      if (!first || !last) return;
      const active = document.activeElement;
      const inside = panel.current.contains(active);
      if (!inside || (e.shiftKey && active === first)) { e.preventDefault(); (e.shiftKey ? last : first).focus(); }
      else if (!e.shiftKey && active === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, []);

  return (
    <div ref={frame} className="fixed inset-0 z-40">
      <button type="button" aria-label="Close menu" tabIndex={-1} onClick={onClose} className="absolute inset-0 bg-black/50" />
      {/* Not showModal(): the browser's top layer and Escape handling would fight ours. */}
      <dialog ref={panel} id={DRAWER_ID} open aria-modal="true" aria-label="Project navigation"
        className="relative inset-auto m-0 flex h-full max-h-none w-[85vw] max-w-xs flex-col overflow-y-auto overscroll-contain border-0 bg-surface-card p-0 text-text-primary shadow-xl">
        <button type="button" aria-label="Close navigation" onClick={onClose}
          className="ml-auto mr-2 mt-2 inline-flex min-h-10 min-w-10 flex-shrink-0 items-center justify-center rounded-lg text-text-secondary hover:bg-surface-hover">
          <X className="w-5 h-5" aria-hidden="true" />
        </button>
        {children}
      </dialog>
    </div>
  );
}
