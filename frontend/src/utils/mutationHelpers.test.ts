import { describe, test, expect, vi, beforeEach } from 'vitest';
import { AxiosError } from 'axios';
import { toast } from 'sonner';
import { onMutationError } from './mutationHelpers';
import { mutationToast } from './toast';

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const refusal = new AxiosError('Request failed with status code 400', 'ERR_BAD_REQUEST', undefined, undefined, {
  status: 400, data: { error: 'type "moneyz" does not exist', status: 400 },
} as never);

describe('mutation error toasts', () => {
  beforeEach(() => vi.clearAllMocks());

  test("onMutationError shows the server's reason", () => {
    onMutationError('create table')(refusal);
    expect(toast.error).toHaveBeenCalledWith('Failed to create table', { description: 'type "moneyz" does not exist' });
  });

  test("mutationToast shows the server's reason", () => {
    mutationToast('Saved', 'Failed to save').onError(refusal);
    expect(toast.error).toHaveBeenCalledWith('Failed to save', { description: 'type "moneyz" does not exist' });
  });
});
