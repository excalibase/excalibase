import { Fragment, useEffect, useRef, type KeyboardEvent } from 'react';
import { cn } from '../../../utils/cn';

export interface AdminTab {
  id: string;
  label: string;
  // Starts a new group of tabs: a thin separator is drawn before it.
  startsGroup?: boolean;
  // A short count or state beside the label, as in "Abuse 98".
  badge?: { text: string; tone: 'neutral' | 'bad' | 'warn' };
}

const BADGE_TONE = {
  neutral: 'bg-bg-secondary text-text-secondary',
  bad: 'bg-red-500/10 text-red-400',
  warn: 'bg-amber-500/10 text-amber-400',
} as const;

const ARROW_STEPS: Record<string, number> = { ArrowRight: 1, ArrowLeft: -1 };

interface AdminTabsProps {
  readonly tabs: readonly AdminTab[];
  readonly active: string;
  readonly onChange: (id: string) => void;
}

// The sections of the admin console. Arrow keys move between them, as in any
// tab list; the active one is the only one in the tab order.
export function AdminTabs({ tabs, active, onChange }: AdminTabsProps) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  // A long bar scrolls sideways, so the chosen tab is brought into view.
  useEffect(() => {
    refs.current[active]?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' });
  }, [active]);
  const move = (event: KeyboardEvent, index: number) => {
    const step = ARROW_STEPS[event.key];
    if (step === undefined) return;
    event.preventDefault();
    const next = tabs[(index + step + tabs.length) % tabs.length];
    onChange(next.id);
    refs.current[next.id]?.focus();
  };
  return (
    <div className="border-b border-border-primary">
      <div role="tablist" aria-label="Admin sections" className="flex flex-nowrap items-center gap-1 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {tabs.map((tab, index) => (
          <Fragment key={tab.id}>
            {tab.startsGroup && <span role="separator" aria-orientation="vertical" className="mx-2 h-5 w-px shrink-0 bg-border-primary" />}
            <button
              ref={(el) => { refs.current[tab.id] = el; }}
              id={`admin-tab-${tab.id}`}
              role="tab"
              type="button"
              aria-selected={tab.id === active}
              aria-controls={`admin-panel-${tab.id}`}
              tabIndex={tab.id === active ? 0 : -1}
              onClick={() => onChange(tab.id)}
              onKeyDown={(event) => move(event, index)}
              className={cn('shrink-0 px-4 min-h-10 text-sm font-medium border-b-2 whitespace-nowrap',
                tab.id === active ? 'border-purple-500 text-text-primary' : 'border-transparent text-text-secondary hover:text-text-primary')}
            >
              {tab.label}
              {tab.badge && ' '}
              {tab.badge && <span className={cn('ml-2 rounded-full px-1.5 py-0.5 text-xs tabular-nums', BADGE_TONE[tab.badge.tone])}>{tab.badge.text}</span>}
            </button>
          </Fragment>
        ))}
      </div>
    </div>
  );
}
