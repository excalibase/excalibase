import { useEffect } from 'react';

export const APP_TITLE = 'Excalibase Studio';

// "<page> · Excalibase Studio"; without a page, just the app name.
export function useDocumentTitle(page?: string): void {
  useEffect(() => {
    document.title = page ? `${page} · ${APP_TITLE}` : APP_TITLE;
  }, [page]);
}
