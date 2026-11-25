import { useCallback, useState } from 'react';

const KEY = 'studio.home.hiddenWidgets';

export function loadHiddenWidgets(): string[] {
  try {
    const parsed: unknown = JSON.parse(localStorage.getItem(KEY) ?? '[]');
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === 'string') : [];
  } catch {
    return [];
  }
}

function saveHiddenWidgets(hidden: readonly string[]): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(hidden));
  } catch {
    // The choice lasts until the page closes.
  }
}

// Which Home widgets this person hid; kept in this browser.
export function useHiddenWidgets() {
  const [hidden, setHidden] = useState<string[]>(loadHiddenWidgets);
  const toggle = useCallback((id: string) => {
    setHidden((prev) => {
      const next = prev.includes(id) ? prev.filter((h) => h !== id) : [...prev, id];
      saveHiddenWidgets(next);
      return next;
    });
  }, []);
  return { hidden, toggle };
}
