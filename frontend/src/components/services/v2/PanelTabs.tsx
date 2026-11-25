import { useRef } from 'react';
import { cn } from '../../../utils/cn';
import type { PanelTab } from './panelState';

interface PanelTabsProps {
  readonly label: string;
  readonly tabs: readonly PanelTab[];
  readonly active: string;
  readonly onSelect: (key: string) => void;
  readonly idPrefix: string;
  readonly children: React.ReactNode;
}

// Tabs with a roving focus: the active tab is the one in the tab order, and the
// arrow keys, Home and End move between the others.
export function PanelTabs({ label, tabs, active, onSelect, idPrefix, children }: PanelTabsProps) {
  const buttons = useRef<Array<HTMLButtonElement | null>>([]);
  const tabId = (key: string) => `${idPrefix}-tab-${key}`;
  const panelId = `${idPrefix}-panel`;

  const move = (event: React.KeyboardEvent, index: number) => {
    const last = tabs.length - 1;
    const target = { ArrowRight: index === last ? 0 : index + 1, ArrowLeft: index === 0 ? last : index - 1, Home: 0, End: last }[event.key];
    if (target === undefined) return;
    event.preventDefault();
    buttons.current[target]?.focus();
    onSelect(tabs[target].key);
  };

  return (
    <div>
      <div role="tablist" aria-label={label} className="flex gap-1 overflow-x-auto border-b border-border-primary">
        {tabs.map((tab, index) => (
          <button
            key={tab.key}
            ref={(node) => {
              buttons.current[index] = node;
            }}
            id={tabId(tab.key)}
            role="tab"
            type="button"
            aria-selected={tab.key === active}
            aria-controls={panelId}
            tabIndex={tab.key === active ? 0 : -1}
            onClick={() => onSelect(tab.key)}
            onKeyDown={(event) => move(event, index)}
            className={cn(
              'whitespace-nowrap border-b-2 px-3 py-2.5 text-sm',
              tab.key === active ? 'border-purple-500 text-text-primary' : 'border-transparent text-text-secondary hover:text-text-primary',
            )}
          >
            {tab.label}
          </button>
        ))}
      </div>
      <div role="tabpanel" id={panelId} aria-labelledby={tabId(active)} className="pt-5">
        {children}
      </div>
    </div>
  );
}
