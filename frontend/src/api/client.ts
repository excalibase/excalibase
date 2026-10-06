import axios from 'axios';
import { API_BASE } from './base';

// The httpOnly `excali_session` cookie set on /api/auth/login is Studio's only
// credential; no script can read it. withCredentials lets the browser send it
// when the API is on another origin in development.
export const api = axios.create({
  baseURL: API_BASE,
  timeout: 30000,
  withCredentials: true,
  headers: {
    'Content-Type': 'application/json',
  },
});

// 401 → drop the cached profile and send the user to sign in. Clearing the
// cookie is the server's job (logout, or the cookie's own expiry).
api.interceptors.response.use(
  (response) => response,
  (error) => {
    // A user function's own 401, relayed by invoke, is its answer, not ours.
    const fromFunction = error.response?.headers?.['x-excalibase-function-response'] === '1';
    if (error.response?.status === 401 && !fromFunction) {
      localStorage.removeItem('auth_user');
      if (globalThis.location.pathname !== '/login') {
        globalThis.location.href = '/login';
      }
    }
    return Promise.reject(error);
  }
);
