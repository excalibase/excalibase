import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { Bell, ChevronDown, CircleHelp, LogOut, Moon, Search, Sun, KeyRound } from 'lucide-react';
import { DOCS_URL } from './navV2';
import { useAuthStore } from '../../../stores/auth-store';
import { useDarkMode } from '../../../hooks/useDarkMode';

const ICON_BUTTON = 'p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover min-w-10 min-h-10 inline-flex items-center justify-center';

// A popup that closes on an outside click or Escape.
export function usePopup() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const outside = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false); };
    const escape = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false); };
    document.addEventListener('mousedown', outside);
    document.addEventListener('keydown', escape);
    return () => { document.removeEventListener('mousedown', outside); document.removeEventListener('keydown', escape); };
  }, [open]);
  return { open, setOpen, ref };
}

export function TopBar({ left, onSearch }: { readonly left: ReactNode; readonly onSearch: () => void }) {
  return (
    <header className="flex flex-wrap items-center gap-3 px-4 py-2 bg-surface-card border-b border-border-primary flex-shrink-0">
      <Link to="/" className="flex items-center gap-2 font-bold text-text-primary" aria-label="Excalibase home">
        <img src="/logo-icon.png" alt="" className="w-6 h-6 object-contain" />
        <span>Excalibase</span>
      </Link>
      {left}
      <button type="button" onClick={onSearch} aria-label="Search"
        className="flex min-w-0 flex-1 basis-64 max-w-xl items-center gap-2 rounded-lg border border-border-primary bg-bg-secondary px-3 min-h-9 text-left text-sm text-text-tertiary hover:bg-surface-hover">
        <Search className="w-4 h-4" aria-hidden="true" />
        <span className="flex-1 truncate">Search projects, services, settings and docs</span>
        <kbd className="rounded border border-border-primary px-1.5 text-[11px]">⌘K</kbd>
      </button>
      <div className="ml-auto flex items-center gap-1">
        <a href={DOCS_URL} target="_blank" rel="noreferrer" aria-label="Help and docs" className={ICON_BUTTON}>
          <CircleHelp className="w-[18px] h-[18px]" aria-hidden="true" />
        </a>
        <Notifications />
        <AccountMenu />
      </div>
    </header>
  );
}

function Notifications() {
  const { open, setOpen, ref } = usePopup();
  return (
    <div ref={ref} className="relative">
      <button type="button" aria-label="Notifications" aria-expanded={open} onClick={() => setOpen(!open)} className={ICON_BUTTON}>
        <Bell className="w-[18px] h-[18px]" aria-hidden="true" />
      </button>
      {open && (
        <div className="absolute right-0 top-full z-30 mt-1 w-64 rounded-xl border border-border-primary bg-surface-card p-4 text-sm text-text-secondary shadow-lg">
          <output className="block">You have no notifications.</output>
          <Link to="/usage" onClick={() => setOpen(false)} className="mt-2 inline-block text-purple-400 hover:underline">Usage &amp; credits</Link>
        </div>
      )}
    </div>
  );
}

function AccountMenu() {
  const { open, setOpen, ref } = usePopup();
  const { user, logout } = useAuthStore();
  const { dark, toggle } = useDarkMode();
  const item = 'flex w-full items-center gap-2 rounded-lg px-3 py-2 text-sm text-text-secondary hover:bg-surface-hover hover:text-text-primary';
  return (
    <div ref={ref} className="relative">
      <button type="button" aria-label="Account menu" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen(!open)}
        className="inline-flex items-center gap-1 rounded-lg border border-border-primary bg-bg-secondary px-3 min-h-9 text-sm text-text-primary">
        {user?.username ?? 'Account'} <ChevronDown className="w-3.5 h-3.5" aria-hidden="true" />
      </button>
      {open && (
        <div role="menu" className="absolute right-0 top-full z-30 mt-1 w-56 rounded-xl border border-border-primary bg-surface-card p-1 shadow-lg">
          {user?.email && <p className="px-3 py-2 text-xs text-text-tertiary truncate">{user.email}</p>}
          <Link role="menuitem" to="/account/tokens" onClick={() => setOpen(false)} className={item}><KeyRound className="w-4 h-4" /> Access keys</Link>
          <button role="menuitem" type="button" onClick={toggle} className={item}>
            {dark ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />} {dark ? 'Light theme' : 'Dark theme'}
          </button>
          <button role="menuitem" type="button" onClick={() => { void logout(); }} className={item}><LogOut className="w-4 h-4" /> Sign out</button>
        </div>
      )}
    </div>
  );
}
