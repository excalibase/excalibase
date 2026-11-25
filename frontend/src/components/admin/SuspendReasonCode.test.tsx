import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SuspendReasonCode, SUSPEND_REASON_CODES } from './SuspendReasonCode';

describe('SuspendReasonCode', () => {
  test('offers exactly the rules the server knows, with the chosen one selected', () => {
    render(<SuspendReasonCode value="crypto_mining" onChange={() => {}} />);
    const select = screen.getByLabelText(/Rule broken/) as HTMLSelectElement;
    expect(select.value).toBe('crypto_mining');
    expect(Array.from(select.options).map((option) => option.value)).toEqual(
      ['proxy_vpn', 'crypto_mining', 'spam_or_phishing', 'disposable_email', 'other'],
    );
    expect(SUSPEND_REASON_CODES).toHaveLength(5);
  });

  test('picking a rule reports its code', async () => {
    const onChange = vi.fn();
    render(<SuspendReasonCode value="other" onChange={onChange} />);
    await userEvent.selectOptions(screen.getByLabelText(/Rule broken/), 'spam_or_phishing');
    expect(onChange).toHaveBeenCalledWith('spam_or_phishing');
  });
});
