import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { EnvField, envTextProblem, textToEnv } from './SiteBuildSettingsForm';

function renderField(env: Record<string, string>) {
  const onChange = vi.fn();
  const onProblem = vi.fn();
  render(
    <EnvField
      label="Vars"
      hint="h"
      env={env}
      testId="vars"
      onChange={onChange}
      onProblem={onProblem}
    />,
  );
  return { onChange, onProblem };
}

describe('textToEnv', () => {
  test('keeps trailing whitespace in a value and reads CRLF lines', () => {
    expect(textToEnv('A=val \r\nB=2\r\n')).toEqual({ A: 'val ', B: '2' });
  });
});

describe('envTextProblem', () => {
  test('names the line, not its text, for a missing = or a bad name', () => {
    expect(envTextProblem('A=1\nsk-live-secret')).toBe(
      'Line 2 is not NAME=value. Nothing is saved until it is fixed.',
    );
    const bad = envTextProblem('my secret=x');
    expect(bad).toContain('Line 1');
    expect(bad).not.toContain('my secret');
  });

  test('accepts comments, blanks and CRLF', () => {
    expect(envTextProblem('# c\r\n\r\nA=1\r\n')).toBeNull();
  });
});

describe('EnvField', () => {
  test('lists a multi-line value as kept, raises no problem, and keeps it on every edit', async () => {
    const { onChange, onProblem } = renderField({ CERT: 'a\nb', X: '1' });
    expect(screen.getByTestId('vars')).toHaveValue('X=1');
    expect(screen.getByTestId('vars-kept')).toHaveTextContent('CERT: multi-line, kept as it is');
    expect(onProblem).not.toHaveBeenCalledWith(expect.any(String));
    await userEvent.type(screen.getByTestId('vars'), '\nY=2');
    await userEvent.tab();
    expect(onChange).toHaveBeenCalledWith({ CERT: 'a\nb', X: '1', Y: '2' });
  });

  test('a value with a carriage return is multi-line too, so it cannot start a new variable', async () => {
    const { onChange } = renderField({ CR: 'left\rRIGHT=injected' });
    expect(screen.getByTestId('vars')).toHaveValue('');
    await userEvent.type(screen.getByTestId('vars'), 'Z=1');
    await userEvent.tab();
    expect(onChange).toHaveBeenCalledWith({ CR: 'left\rRIGHT=injected', Z: '1' });
  });

  test('typing the name of a kept variable replaces it', async () => {
    const { onChange } = renderField({ CERT: 'a\nb' });
    await userEvent.type(screen.getByTestId('vars'), 'CERT=replaced');
    await userEvent.tab();
    expect(onChange).toHaveBeenCalledWith({ CERT: 'replaced' });
    expect(screen.queryByTestId('vars-kept')).toBeNull();
  });

  test('tabbing through leaves a value with trailing space as it was', async () => {
    const { onChange } = renderField({ X: 'val ' });
    await userEvent.click(screen.getByTestId('vars'));
    await userEvent.tab();
    expect(onChange).toHaveBeenCalledWith({ X: 'val ' });
  });
});
