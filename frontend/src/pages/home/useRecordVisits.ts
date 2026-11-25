import { useEffect } from 'react';
import { useLocation } from 'react-router-dom';
import { recordVisit } from './recentlyVisited';

// Remembers the pages a person opens, in this browser only, for the Recently visited widget.
export function useRecordVisits(): void {
  const { pathname } = useLocation();
  useEffect(() => recordVisit(pathname), [pathname]);
}
