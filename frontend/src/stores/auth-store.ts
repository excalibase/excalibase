import { create } from 'zustand';
import { api } from '../api/client';

export interface AuthUser {
  id: string;
  username: string;
  email: string;
  role: string;
}

interface AuthState {
  // user: profile fetched from /api/auth/me (kept in localStorage for
  // bootstrapping the UI before the cookie round-trip). Never includes
  // the access token in the new flow — the httpOnly cookie owns that.
  user: AuthUser | null;
  isAuthenticated: boolean;
  // setAuth is called after a successful login / register. Token is
  // optional: when missing (cookie flow) we trust the cookie. When
  // present (legacy code paths still passing the body token) we keep
  // it in localStorage as a header fallback.
  setAuth: (user: AuthUser, opts?: { legacyToken?: string }) => void;
  // clearAuth wipes local state immediately. The matching server-side
  // revocation goes through `logout()` below.
  clearAuth: () => void;
  // logout: hits the platform's /auth/logout (revokes the session PAT,
  // clears the httpOnly cookie) then wipes any legacy localStorage.
  logout: () => Promise<void>;
}

const USER_KEY = 'auth_user';
const LEGACY_TOKEN_KEY = 'auth_token';

function loadFromStorage(): Pick<AuthState, 'user' | 'isAuthenticated'> {
  try {
    const userJson = localStorage.getItem(USER_KEY);
    if (userJson) {
      const user = JSON.parse(userJson) as AuthUser;
      // If we have a stored user we're optimistically authenticated. The
      // first API call will 401 if the cookie has expired and the axios
      // interceptor will redirect to /login. This avoids a flash of the
      // login page on every refresh.
      return { user, isAuthenticated: true };
    }
  } catch {
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
  }
  return { user: null, isAuthenticated: false };
}

export const useAuthStore = create<AuthState>((set) => ({
  ...loadFromStorage(),

  setAuth: (user, opts) => {
    localStorage.setItem(USER_KEY, JSON.stringify(user));
    if (opts?.legacyToken) {
      localStorage.setItem(LEGACY_TOKEN_KEY, opts.legacyToken);
    }
    set({ user, isAuthenticated: true });
  },

  clearAuth: () => {
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
    set({ user: null, isAuthenticated: false });
  },

  logout: async () => {
    // Tell the server to revoke the session PAT and clear the cookie.
    // Best-effort: even if the call fails (network drop, expired cookie)
    // we still wipe local state so the user can't get stuck.
    try {
      await api.post('/auth/logout');
    } catch {
      // Ignored — local logout still proceeds.
    }
    localStorage.removeItem(USER_KEY);
    localStorage.removeItem(LEGACY_TOKEN_KEY);
    set({ user: null, isAuthenticated: false });
  },
}));
