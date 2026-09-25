import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { api } from '../api/client';

interface SetupStatus {
  hasAdmin: boolean;
}

interface RegisterRequest {
  username: string;
  email: string;
  password: string;
  // Required for the very first registration (EXC-451): the server prints a
  // one-time token to its log at startup when no platform admin exists yet.
  // Ignored by the backend once an admin already exists.
  setupToken?: string;
}

interface RegisterResponse {
  token: string;
  user: {
    id: string;
    username: string;
    email: string;
    role: string;
  };
}

/**
 * Polls the public /api/auth/setup-status endpoint to determine whether
 * the platform has at least one user (admin). The wizard renders the
 * create-admin step when hasAdmin=false. Re-polls every 5s while empty
 * so the wizard can react to a register call from another tab.
 */
export function useSetupStatus() {
  return useQuery<SetupStatus>({
    queryKey: ['setup', 'status'],
    queryFn: async () => {
      const { data } = await api.get('/auth/setup-status');
      return data;
    },
    staleTime: 5_000,
    refetchInterval: (q) => (q.state.data?.hasAdmin === false ? 5_000 : false),
  });
}

/**
 * Wizard-step variant of POST /api/auth/register. Backend auto-promotes
 * the first registration to platform_admin, so the wizard does not need
 * to set role explicitly. Returns the issued PAT once — the studio
 * stashes it and uses it for subsequent calls.
 */
export function useRegisterAdmin() {
  const queryClient = useQueryClient();
  return useMutation<RegisterResponse, Error, RegisterRequest>({
    mutationFn: async (req) => {
      const { data } = await api.post<RegisterResponse>('/auth/register', req);
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['setup', 'status'] });
    },
  });
}
