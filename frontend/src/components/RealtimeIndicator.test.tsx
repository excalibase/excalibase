import { describe, test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { RealtimeIndicator } from './RealtimeIndicator';

describe('RealtimeIndicator', () => {
  test('renders "Live" with a green dot when status is live', () => {
    render(<RealtimeIndicator status="live" />);
    const badge = screen.getByTestId('realtime-indicator');
    expect(badge).toHaveTextContent('Live');
    expect(badge.className).toMatch(/green/);
  });

  test('renders "Connecting..." in blue when status is connecting', () => {
    render(<RealtimeIndicator status="connecting" />);
    const badge = screen.getByTestId('realtime-indicator');
    expect(badge).toHaveTextContent(/Connecting/i);
    expect(badge.className).toMatch(/blue/);
  });

  test('renders "Reconnecting..." in yellow/amber when status is reconnecting', () => {
    render(<RealtimeIndicator status="reconnecting" />);
    const badge = screen.getByTestId('realtime-indicator');
    expect(badge).toHaveTextContent(/Reconnecting/i);
    expect(badge.className).toMatch(/amber|yellow/);
  });

  test('renders "Offline" in red when status is offline', () => {
    render(<RealtimeIndicator status="offline" />);
    const badge = screen.getByTestId('realtime-indicator');
    expect(badge).toHaveTextContent(/Offline/i);
    expect(badge.className).toMatch(/red/);
  });

  test('forwards an extra className from the consumer', () => {
    render(<RealtimeIndicator status="live" className="ml-4" />);
    expect(screen.getByTestId('realtime-indicator').className).toMatch(/ml-4/);
  });
});
