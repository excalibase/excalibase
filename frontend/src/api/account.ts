import { useQuery } from '@tanstack/react-query';
import { api } from './client';

// The signed-in person, as GET /api/auth/me reports them.
export interface Me {
  id: string;
  username: string;
  email: string;
  role: string;
  emailVerifiedAt?: string;
}

export interface ResetSent {
  expiresInMinutes?: number;
}

export const useMe = () =>
  useQuery({ queryKey: ['me'], queryFn: async () => (await api.get<Me>('/auth/me')).data, staleTime: 30_000 });

// The same mail the sign-in page's "Forgot password" sends: the password itself is set on the emailed link's page.
export const sendPasswordReset = async (email: string): Promise<ResetSent> =>
  (await api.post<ResetSent>('/email/reset/send', { email })).data ?? {};

export const sendVerificationEmail = async (): Promise<void> => {
  await api.post('/email/verify/send');
};

export function mailFailure(err: unknown, fallback: string): string {
  const status = (err as { response?: { status?: number } }).response?.status;
  return status === 429 ? 'Too many requests. Wait a while and try again.' : fallback;
}
