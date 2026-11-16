import { describe, test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { OneTimePasswordPanel } from './OneTimePasswordPanel';
import type { ConnectAddress } from './ConnectionParts';

const PASSWORD = 'test-password-not-real';

function Harness({ address }: { readonly address: ConnectAddress }) {
  const [open, setOpen] = useState(true);
  return open ? (
    <OneTimePasswordPanel
      projectId="p-1" password={PASSWORD} address={address}
      database="appdb" user="app" sslmode="require" onDone={() => setOpen(false)}
    />
  ) : <p>saved</p>;
}

const PUBLIC: ConnectAddress = { kind: 'public', host: 'p-1.db.excalibase.io', port: 26257 };

function recordClipboard(): string[] {
  const written: string[] = [];
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: (text: string) => { written.push(text); return Promise.resolve(); } },
  });
  return written;
}

describe('OneTimePasswordPanel', () => {
  test('shows the connection details and the password with a one-time warning', () => {
    render(<Harness address={PUBLIC} />);
    expect(screen.getByTestId('otp-password')).toHaveTextContent(PASSWORD);
    expect(screen.getByTestId('otp-host')).toHaveTextContent('p-1.db.excalibase.io');
    expect(screen.getByTestId('otp-port')).toHaveTextContent('26257');
    expect(screen.getByTestId('otp-database')).toHaveTextContent('appdb');
    expect(screen.getByTestId('otp-user')).toHaveTextContent('app');
    expect(screen.getByTestId('otp-sslmode')).toHaveTextContent('require');
    expect(screen.getByText('Copy it now. It will not be shown again.')).toBeInTheDocument();
  });

  test('copies the password', async () => {
    const user = userEvent.setup();
    render(<Harness address={PUBLIC} />);
    const written = recordClipboard();
    await user.click(screen.getByRole('button', { name: /^copy/i }));
    expect(written).toEqual([PASSWORD]);
  });

  test('offers a CSV download with the header and the row', () => {
    render(<Harness address={PUBLIC} />);
    const link = screen.getByRole('link', { name: /download csv/i });
    expect(link).toHaveAttribute('download', 'p-1-database-credentials.csv');
    const csv = decodeURIComponent(link.getAttribute('href')!.split(',').slice(1).join(','));
    expect(csv).toBe(
      'host,port,database,user,password,sslmode\n"p1.db.excalibase.io","26257","appdb","app","test-password-not-real","require"\n'
        .replace('p1.', 'p-1.'),
    );
  });

  test('drops the password from the page once saved', async () => {
    const user = userEvent.setup();
    const { container } = render(<Harness address={PUBLIC} />);
    await user.click(screen.getByRole('button', { name: /i've saved it/i }));
    expect(screen.getByText('saved')).toBeInTheDocument();
    expect(container.innerHTML).not.toContain(PASSWORD);
    expect(document.body.innerHTML).not.toContain(encodeURIComponent(PASSWORD));
  });

  test('with no public endpoint the panel host says so and the CSV host cell is empty', () => {
    render(<Harness address={{ kind: 'none' }} />);
    expect(screen.getByTestId('otp-host')).toHaveTextContent('Not reachable from the internet');
    const csv = decodeURIComponent(screen.getByRole('link', { name: /download csv/i }).getAttribute('href')!);
    expect(csv.split('\n')[1].startsWith('"","",')).toBe(true);
    expect(csv).not.toContain('not reachable');
  });
});
