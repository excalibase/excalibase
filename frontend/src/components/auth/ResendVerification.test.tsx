import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ResendVerification } from './ResendVerification';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({ api: { post: vi.fn() } }));

const tooMany = {
  message: 'Request failed with status code 429',
  response: { status: 429, data: { error: 'too many links sent; wait a minute', status: 429 } },
};

const sendButton = () => screen.getByRole('button', { name: 'Send a new link' });

describe('ResendVerification', () => {
  beforeEach(() => vi.clearAllMocks());

  test('an empty or blank address cannot be sent', () => {
    render(<ResendVerification />);
    expect(sendButton()).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Your email'), { target: { value: '   ' } });
    expect(sendButton()).toBeDisabled();
  });

  test('sends the trimmed address and answers the same whether or not it exists', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    render(<ResendVerification />);
    fireEvent.change(screen.getByLabelText('Your email'), { target: { value: ' dev@acme.io ' } });
    fireEvent.click(sendButton());
    expect(await screen.findByText(/a new link is on its way/)).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/email/verify/resend', { email: 'dev@acme.io' });
  });

  test('a likely address is prefilled and still editable', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    render(<ResendVerification initialEmail="dev@acme.io" />);
    const field = screen.getByLabelText('Your email');
    expect(field).toHaveValue('dev@acme.io');
    fireEvent.change(field, { target: { value: 'ops@acme.io' } });
    fireEvent.click(sendButton());
    expect(await screen.findByText(/a new link is on its way/)).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/email/verify/resend', { email: 'ops@acme.io' });
  });

  test('a known address needs no field', () => {
    render(<ResendVerification email="dev@acme.io" />);
    expect(screen.queryByLabelText('Your email')).not.toBeInTheDocument();
    expect(sendButton()).toBeEnabled();
  });

  test('a refusal shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(tooMany);
    render(<ResendVerification email="dev@acme.io" />);
    fireEvent.click(sendButton());
    expect(await screen.findByRole('alert')).toHaveTextContent('too many links sent; wait a minute');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick clicks send one link', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}));
    render(<ResendVerification email="dev@acme.io" />);
    fireEvent.click(sendButton());
    fireEvent.click(sendButton());
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });
});
