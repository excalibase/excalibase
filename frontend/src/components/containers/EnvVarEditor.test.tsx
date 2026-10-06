import { describe, test, expect, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { EnvVarEditor } from './EnvVarEditor';
import { emptyEnvRow, type EnvRow } from './appFormModel';

const row = (patch: Partial<EnvRow>): EnvRow => ({ ...emptyEnvRow(), ...patch });

function mount(rows: EnvRow[], extra: { errors?: Record<number, string>; databaseName?: string } = {}) {
  const onChange = vi.fn();
  render(<EnvVarEditor rows={rows} onChange={onChange} {...extra} />);
  return onChange;
}

describe('EnvVarEditor', () => {
  test('with no rows it says the container starts with none and can add one', () => {
    const onChange = mount([]);
    expect(screen.getByText(/no variables yet/i)).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('env-add'));
    expect(onChange).toHaveBeenCalledWith([expect.objectContaining({ name: '', kind: 'literal' })]);
  });

  test('editing a name or value changes only that row', () => {
    const first = row({ name: 'A', value: '1' });
    const second = row({ name: 'B', value: '2' });
    const onChange = mount([first, second]);
    fireEvent.change(screen.getByTestId('env-name-1'), { target: { value: 'API_URL' } });
    expect(onChange).toHaveBeenLastCalledWith([first, { ...second, name: 'API_URL' }]);
    fireEvent.change(screen.getByTestId('env-value-0'), { target: { value: 'x' } });
    expect(onChange).toHaveBeenLastCalledWith([{ ...first, value: 'x' }, second]);
  });

  test('a row error from validation is shown under its row', () => {
    mount([row({ name: '1BAD' })], { errors: { 0: 'Names start with a letter or underscore.' } });
    expect(screen.getByTestId('env-row-0')).toHaveTextContent('Names start with a letter or underscore.');
  });

  test('removing a row drops it', () => {
    const first = row({ name: 'A' });
    const second = row({ name: 'B' });
    const onChange = mount([first, second]);
    fireEvent.click(screen.getByTestId('env-remove-0'));
    expect(onChange).toHaveBeenCalledWith([second]);
  });

  test('a database connection can only be chosen when the project has a database', () => {
    mount([row({})]);
    expect(screen.getByRole('option', { name: 'Database connection' })).toBeDisabled();
  });

  test('a reference row picks a database variable', () => {
    const reference = row({ kind: 'reference' });
    const onChange = mount([reference], { databaseName: 'main' });
    expect(screen.getByTestId('env-row-0')).toHaveTextContent('database main');
    fireEvent.change(screen.getByTestId('env-variable-0'), { target: { value: 'PGHOST' } });
    expect(onChange).toHaveBeenCalledWith([{ ...reference, variable: 'PGHOST' }]);
  });

  test('a new secret takes a hidden value', () => {
    const secret = row({ kind: 'secret', name: 'TOKEN' });
    const onChange = mount([secret]);
    const input = screen.getByTestId('env-secret-value-0');
    expect(input).toHaveAttribute('type', 'password');
    fireEvent.change(input, { target: { value: 's3cret' } });
    expect(onChange).toHaveBeenCalledWith([{ ...secret, secretValue: 's3cret' }]);
  });

  test('a stored secret reads Set, can be replaced, and replacing can be undone', () => {
    const stored = row({ kind: 'secret', name: 'TOKEN', storedName: 'TOKEN', storedSecret: { path: 'app-secret', key: 'TOKEN' } });
    const onChange = mount([stored]);
    expect(screen.getByTestId('env-secret-set-0')).toHaveTextContent('Set');
    fireEvent.click(screen.getByTestId('env-secret-replace-0'));
    expect(onChange).toHaveBeenCalledWith([{ ...stored, replacing: true, secretValue: '' }]);
  });

  test('while replacing a stored secret, keep current restores it', () => {
    const replacing = row({
      kind: 'secret',
      name: 'TOKEN',
      storedName: 'TOKEN',
      storedSecret: { path: 'app-secret', key: 'TOKEN' },
      replacing: true,
    });
    const onChange = mount([replacing]);
    expect(screen.getByTestId('env-secret-value-0')).toHaveAttribute('placeholder', 'new secret value');
    fireEvent.click(screen.getByTestId('env-secret-keep-0'));
    expect(onChange).toHaveBeenCalledWith([{ ...replacing, replacing: false, secretValue: '' }]);
  });

  test('changing the kind of a row', () => {
    const literal = row({ name: 'A' });
    const onChange = mount([literal]);
    fireEvent.change(screen.getByTestId('env-kind-0'), { target: { value: 'secret' } });
    expect(onChange).toHaveBeenCalledWith([{ ...literal, kind: 'secret' }]);
  });
});
