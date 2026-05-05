import axios from 'axios';

// withCredentials lets the browser attach the httpOnly `excali_session`
// cookie that the backend sets on /api/auth/login. The cookie is the
// primary auth path for the studio — XSS in this app cannot read it.
//
// The Authorization header path is still supported for:
//   - long-lived CI/script PATs (no browser, no cookie jar)
//   - existing localStorage sessions left over from before the cookie flow
//     (graceful degradation; will be removed once all users have logged in
//     fresh after the cookie flow ships)
export const api = axios.create({
  baseURL: import.meta.env.VITE_API_URL || 'http://localhost:24005/api',
  timeout: 30000,
  withCredentials: true,
  headers: {
    'Content-Type': 'application/json',
  },
});

// Header fallback for legacy localStorage tokens. The cookie supersedes this
// for new sessions, but until everyone re-logs we keep both paths working.
// If the server sees both, the header wins (see auth.extractRawToken in Go).
api.interceptors.request.use((config) => {
  const token = localStorage.getItem('auth_token');
  if (token && !config.headers.Authorization) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

// 401 → drop legacy token + redirect. The cookie clear is the server's job
// (Logout endpoint or stale-cookie auto-expiry).
api.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error.response?.status === 401) {
      localStorage.removeItem('auth_token');
      localStorage.removeItem('auth_user');
      if (globalThis.location.pathname !== '/login') {
        globalThis.location.href = '/login';
      }
    }
    return Promise.reject(error);
  }
);
