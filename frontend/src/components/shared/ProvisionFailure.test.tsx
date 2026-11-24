import { describe, test, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ProvisionFailure } from './ProvisionFailure';
import { useAuthStore } from '../../stores/auth-store';

const signIn = (role: string) =>
  useAuthStore.setState({ user: { id: 'u1', username: 'u', email: 'u@e.test', role }, isAuthenticated: true });

describe('ProvisionFailure', () => {
  beforeEach(() => signIn('user'));

  test('shows the plain wording, never the raw text, to a customer', () => {
    render(<ProvisionFailure reason="pod db-1 CrashLoopBackOff on node n7" />);
    expect(screen.getByText(/kept crashing/i)).toBeInTheDocument();
    expect(screen.queryByText(/db-1/)).not.toBeInTheDocument();
    expect(screen.queryByText('Technical details')).not.toBeInTheDocument();
  });

  test('an unknown text gives the generic wording and no raw text', () => {
    render(<ProvisionFailure reason="secret-internal-detail" />);
    expect(screen.getByText('Provisioning failed. Delete it and add it again. If it keeps failing, tell us.')).toBeInTheDocument();
    expect(screen.queryByText(/secret-internal-detail/)).not.toBeInTheDocument();
  });

  test('a platform admin gets the raw text and extra detail in a collapsed block', () => {
    signIn('platform_admin');
    render(<ProvisionFailure reason="pod db-1 CrashLoopBackOff" detail={<p>rollback line</p>} />);
    const block = screen.getByText('Technical details').closest('details');
    expect(block).not.toHaveAttribute('open');
    expect(block).toHaveTextContent('pod db-1 CrashLoopBackOff');
    expect(block).toHaveTextContent('rollback line');
  });

  test('a platform operator gets the technical details too', () => {
    signIn('platform_operator');
    render(<ProvisionFailure reason="pod db-1 CrashLoopBackOff" />);
    expect(screen.getByText('Technical details').closest('details')).toHaveTextContent('pod db-1 CrashLoopBackOff');
  });

  test('an admin with no raw text and no detail gets no block', () => {
    signIn('platform_admin');
    render(<ProvisionFailure reason={undefined} />);
    expect(screen.getByText(/Provisioning failed/)).toBeInTheDocument();
    expect(screen.queryByText('Technical details')).not.toBeInTheDocument();
  });

  test('a customer never gets the extra detail', () => {
    render(<ProvisionFailure reason="x" detail={<p>rollback line</p>} />);
    expect(screen.queryByText('rollback line')).not.toBeInTheDocument();
  });
});
