import { describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { BoolExpEditor } from './BoolExpEditor';

function renderEditor(overrides: Partial<Parameters<typeof BoolExpEditor>[0]> = {}) {
  const onChange = vi.fn();
  render(
    <BoolExpEditor
      id="filter"
      label="Row filter"
      help="Which rows the role can read."
      value=""
      columns={['id', 'owner_id']}
      onChange={onChange}
      {...overrides}
    />,
  );
  return onChange;
}

describe('BoolExpEditor', () => {
  test('typed text is passed up as is', () => {
    const onChange = renderEditor();
    fireEvent.change(screen.getByLabelText('Row filter'), { target: { value: '{"id":{"_eq":1}}' } });
    expect(onChange).toHaveBeenCalledWith('{"id":{"_eq":1}}');
  });

  test('"Without any checks" fills the empty expression', () => {
    const onChange = renderEditor();
    fireEvent.click(screen.getByRole('button', { name: 'Without any checks' }));
    expect(onChange).toHaveBeenCalledWith('{}');
  });

  test('"Owner only" needs an owner column, then compares it to the user id', () => {
    const onChange = renderEditor();
    const ownerOnly = screen.getByRole('button', { name: 'Owner only' });
    expect(ownerOnly).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Owner column for Row filter'), { target: { value: 'owner_id' } });
    fireEvent.click(ownerOnly);

    expect(JSON.parse(onChange.mock.calls[0][0] as string)).toEqual({ owner_id: { _eq: 'X-Excalibase-User-Id' } });
  });

  test('a validation error is shown and tied to the field', () => {
    renderEditor({ value: '{', error: 'not valid JSON' });
    expect(screen.getByTestId('filter-error')).toHaveTextContent('not valid JSON');
    const field = screen.getByLabelText('Row filter');
    expect(field).toHaveAttribute('aria-invalid', 'true');
    expect(field).toHaveAttribute('aria-describedby', 'perm-filter-error');
  });

  test('no error, no message', () => {
    renderEditor({ value: '{}' });
    expect(screen.queryByTestId('filter-error')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Row filter')).toHaveAttribute('aria-invalid', 'false');
  });
});

// EXC-680: with MFA on, one click requires the second sign-in step.
describe('BoolExpEditor second step', () => {
  test('without MFA there is no second-step button', () => {
    renderEditor();
    expect(screen.queryByRole('button', { name: 'Require the second step (MFA)' })).not.toBeInTheDocument();
  });

  test('with MFA it keeps what is there and requires aal2', () => {
    const onChange = renderEditor({ mfa: true, value: '{"owner_id":{"_eq":"X-Excalibase-User-Id"}}' });
    fireEvent.click(screen.getByRole('button', { name: 'Require the second step (MFA)' }));
    expect(JSON.parse(onChange.mock.calls[0][0] as string)).toEqual({
      _and: [{ owner_id: { _eq: 'X-Excalibase-User-Id' } }, { 'X-Excalibase-Aal': { _eq: 'aal2' } }],
    });
  });
});
