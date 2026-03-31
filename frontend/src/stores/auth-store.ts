import { create } from 'zustand';

export interface AuthUser {
  id: string;
  username: string;
  email: string;
  role: string;
}

interface AuthState {
  accessToken: string | null;
  user: AuthUser | null;
  isAuthenticated: boolean;
  setAuth: (token: string, user: AuthUser) => void;
  clearAuth: () => void;
}

function loadFromStorage(): Pick<AuthState, 'accessToken' | 'user' | 'isAuthenticated'> {
  try {
    const token = localStorage.getItem('auth_token');
    const userJson = localStorage.getItem('auth_user');
    if (token && userJson) {
      const user = JSON.parse(userJson) as AuthUser;
      return { accessToken: token, user, isAuthenticated: true };
    }
  } catch {
    localStorage.removeItem('auth_token');
    localStorage.removeItem('auth_user');
  }
  return { accessToken: null, user: null, isAuthenticated: false };
}

export const useAuthStore = create<AuthState>((set) => ({
  ...loadFromStorage(),

  setAuth: (token: string, user: AuthUser) => {
    localStorage.setItem('auth_token', token);
    localStorage.setItem('auth_user', JSON.stringify(user));
    set({ accessToken: token, user, isAuthenticated: true });
  },

  clearAuth: () => {
    localStorage.removeItem('auth_token');
    localStorage.removeItem('auth_user');
    set({ accessToken: null, user: null, isAuthenticated: false });
  },
}));
