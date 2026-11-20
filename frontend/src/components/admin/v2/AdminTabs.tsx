import { useRef, type KeyboardEvent } from 'react';
import { cn } from '../../../utils/cn';

export interface AdminTab {
  id: string;
  label: string;
}

interface AdminTabsProps {
  readonly tabs: readonly AdminTab[];
  readonly active: string;
  readonly onChange: (id: string) => void;
}

// The sections of the admin console. Arrow keys move between them, as in any
// tab list; the active one is the only one in the tab order.
export function AdminTabs({ tabs, active, onChange }: AdminTabsProps) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  const move = (event: KeyboardEvent, index: number) => {
    const step = event.key === 'ArrowRight' ? 1 : event.key === 'ArrowLeft' ? -1 : 0;
    if (step === 0) return;
    event.preventDefault();
    const next = tabs[(index + step + tabs.length) % tabs.length];
    onChange(next.id);
    refs.current[next.id]?.focus();
  };
  return (
    <div role="tablist" aria-label="Admin sections" className="flex flex-wrap gap-1 border-b border-border-primary">
      {tabs.map((tab, index) => (
        <button
          key={tab.id}
          ref={(el) => { refs.current[tab.id] = el; }}
          id={`admin-tab-${tab.id}`}
          role="tab"
          type="button"
          aria-selected={tab.id === active}
          aria-controls={`admin-panel-${tab.id}`}
          tabIndex={tab.id === active ? 0 : -1}
          onClick={() => onChange(tab.id)}
          onKeyDown={(event) => move(event, index)}
          className={cn('px-4 min-h-10 text-sm font-medium border-b-2 -mb-px',
            tab.id === active ? 'border-purple-500 text-text-primary' : 'border-transparent text-text-secondary hover:text-text-primary')}
        >
          {tab.label}
        </button>
      ))}
    </div>
  );
}
