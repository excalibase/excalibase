import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AppBuildSettingsForm } from './AppBuildSettingsForm';
import { EMPTY_APP_SETTINGS } from '../../api/appBuilds';

describe('AppBuildSettingsForm', () => {
  test('offers the builders the server names and reports a change', async () => {
    const onChange = vi.fn();
    render(
      <AppBuildSettingsForm
        settings={EMPTY_APP_SETTINGS}
        builders={['auto', 'dockerfile', 'buildpacks']}
        onChange={onChange}
      />,
    );
    expect(screen.getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Automatic',
      'Dockerfile',
      'Buildpacks',
    ]);
    await userEvent.selectOptions(screen.getByTestId('app-build-builder'), 'buildpacks');
    expect(onChange).toHaveBeenCalledWith({ ...EMPTY_APP_SETTINGS, builder: 'buildpacks' });
  });
});
