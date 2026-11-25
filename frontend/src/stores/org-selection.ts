import { create } from 'zustand';

const KEY = 'studio.org';

function read(): string | null {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
}

interface OrgSelection {
  orgId: string | null;
  select: (orgId: string) => void;
}

// The org the person last picked; kept per browser, never trusted as a membership
// (the switcher only offers orgs the person belongs to).
export const useOrgSelection = create<OrgSelection>((set) => ({
  orgId: read(),
  select: (orgId) => {
    try {
      localStorage.setItem(KEY, orgId);
    } catch {
      // The choice lasts until the page closes.
    }
    set({ orgId });
  },
}));
