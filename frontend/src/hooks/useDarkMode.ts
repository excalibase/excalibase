import { useState, useEffect } from 'react';

const STORAGE_KEY = 'theme-dark';

export function useDarkMode() {
  const [dark, setDark] = useState(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    // When no stored preference, default to dark mode.
    return stored === null ? true : stored === 'true';
  });

  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark);
    localStorage.setItem(STORAGE_KEY, String(dark));
  }, [dark]);

  const toggle = () => setDark((prev) => !prev);

  return { dark, toggle };
}
