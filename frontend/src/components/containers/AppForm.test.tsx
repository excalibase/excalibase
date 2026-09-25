import { describe, test, expect, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AppForm } from './AppForm';
import type { App } from '../../api/apps';

const existingApp: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [{ name: 'MODE', kind: 'literal', value: 'production' }],
  port: 3000,
  healthCheckPath: '/healthz',
  replicas: 2,
  tier: 'STANDARD',
  status: 'PROVISIONING',
  version: 4,
  createdAt: '',
  updatedAt: '',
};

function renderForm(props: Partial<React.ComponentProps<typeof AppForm>> = {}) {
  const onSubmit = vi.fn();
  render(
    <AppForm
      tier="STANDARD"
      databaseName="appdb"
      submitLabel="Create container"
      submitting={false}
      onSubmit={onSubmit}
      onCancel={() => {}}
      {...props}
    />,
  );
  return { onSubmit, user: userEvent.setup() };
}

describe('AppForm', () => {
  test('starts with port 8080 and one replica', () => {
    renderForm();
    expect(screen.getByTestId('app-port')).toHaveValue(8080);
    expect(screen.getByTestId('app-replicas')).toHaveValue('1');
  });

  test('asks for an image before anything else can be sent', async () => {
    const { onSubmit, user } = renderForm();
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/enter the image to run/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('refuses an image without a tag, in plain words', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx');
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/add a tag or digest/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('names the container after the image until the name is edited', async () => {
    const { user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'ghcr.io/acme/My_Web:1.0');
    expect(screen.getByTestId('app-name')).toHaveValue('my-web');
  });

  test('refuses a port outside 1-65535 and a health path without a leading slash', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx:1.27');
    await user.clear(screen.getByTestId('app-port'));
    await user.type(screen.getByTestId('app-port'), '70000');
    await user.type(screen.getByTestId('app-health'), 'healthz');
    await user.click(screen.getByTestId('app-submit'));
    expect(
      screen.getByText(/port must be a whole number between 1 and 65535/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/health check path must start with \//i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('only offers as many replicas as the project size allows', () => {
    renderForm({ tier: 'FREE' });
    const options = Array.from(screen.getByTestId('app-replicas').querySelectorAll('option')).map(
      (o) => o.value,
    );
    expect(options).toEqual(['0', '1']);
  });

  test('shows the size the project plan gives, in plain words', () => {
    renderForm({ tier: 'STANDARD' });
    expect(screen.getByTestId('app-size')).toHaveTextContent(/1 CPU/);
    expect(screen.getByTestId('app-size')).toHaveTextContent(/1 GB/);
  });

  test('refuses blank and duplicate variable names', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('env-add'));
    await user.click(screen.getByTestId('env-add'));
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getAllByText(/give the variable a name/i)).toHaveLength(2);
    await user.type(screen.getByTestId('env-name-0'), 'MODE');
    await user.type(screen.getByTestId('env-name-1'), 'MODE');
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/MODE is used twice/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('sends literal and database variables in the app, and a new secret value separately', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx:1.27');

    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'MODE');
    await user.type(screen.getByTestId('env-value-0'), 'production');

    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-1'), 'DATABASE_URL');
    await user.selectOptions(screen.getByTestId('env-kind-1'), 'reference');

    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-2'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-2'), 'secret');
    await user.type(screen.getByTestId('env-secret-value-2'), 'sk_live_123');

    await user.click(screen.getByTestId('env-add'));
    await user.click(screen.getByTestId('env-remove-3'));

    await user.click(screen.getByTestId('app-submit'));
    expect(onSubmit).toHaveBeenCalledWith({
      input: {
        name: 'nginx',
        image: 'nginx:1.27',
        port: 8080,
        replicas: 1,
        healthCheckPath: '',
        env: [
          { name: 'MODE', kind: 'literal', value: 'production' },
          {
            name: 'DATABASE_URL',
            kind: 'reference',
            reference: { sourceKind: 'database', sourceName: 'appdb', variable: 'DATABASE_URL' },
          },
        ],
      },
      secrets: [{ name: 'API_KEY', value: 'sk_live_123' }],
    });
  });

  test('a secret value is typed into a masked box', async () => {
    const { user } = renderForm();
    await user.click(screen.getByTestId('env-add'));
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    expect(screen.queryByTestId('env-value-0')).not.toBeInTheDocument();
    expect(screen.getByTestId('env-secret-value-0')).toHaveAttribute('type', 'password');
  });

  test('refuses a new secret without a value', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/enter the secret value/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('a stored secret shows as set and is only ever replaced, never shown', async () => {
    const withSecret: App = {
      ...existingApp,
      env: [
        {
          name: 'API_KEY',
          kind: 'secret',
          secret: { path: 'projects/proj-1/apps/app-1/env/API_KEY', key: 'value' },
        },
      ],
    };
    const { onSubmit, user } = renderForm({ initial: withSecret, submitLabel: 'Save' });
    expect(screen.getByTestId('env-secret-set-0')).toHaveTextContent('Set');
    expect(screen.queryByTestId('env-secret-value-0')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('app-submit'));
    expect(onSubmit).toHaveBeenLastCalledWith({
      input: expect.objectContaining({ env: withSecret.env }),
      secrets: [],
    });

    await user.click(screen.getByTestId('env-secret-replace-0'));
    expect(screen.getByTestId('env-secret-value-0')).toHaveValue('');
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/enter the secret value/i)).toBeInTheDocument();

    await user.click(screen.getByTestId('env-secret-keep-0'));
    expect(screen.getByTestId('env-secret-set-0')).toBeInTheDocument();
    await user.click(screen.getByTestId('env-secret-replace-0'));
    await user.type(screen.getByTestId('env-secret-value-0'), 'rotated');
    await user.click(screen.getByTestId('app-submit'));
    expect(onSubmit).toHaveBeenLastCalledWith({
      input: expect.objectContaining({ env: withSecret.env }),
      secrets: [{ name: 'API_KEY', value: 'rotated' }],
    });
  });

  test('refuses a secret value longer than 8 KB', async () => {
    const { onSubmit, user } = renderForm();
    await user.type(screen.getByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    fireEvent.change(screen.getByTestId('env-secret-value-0'), {
      target: { value: 'x'.repeat(8193) },
    });
    await user.click(screen.getByTestId('app-submit'));
    expect(screen.getByText(/secret value is too long/i)).toBeInTheDocument();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  test('cannot pick a database connection when the project has no database', async () => {
    const { user } = renderForm({ databaseName: undefined });
    await user.click(screen.getByTestId('env-add'));
    const reference = screen.getByTestId('env-kind-0').querySelector('option[value="reference"]');
    expect(reference).toBeDisabled();
  });

  test('edits an existing container, keeping its values', async () => {
    const { onSubmit, user } = renderForm({ initial: existingApp, submitLabel: 'Save' });
    expect(screen.getByTestId('app-name')).toHaveValue('web');
    await user.clear(screen.getByTestId('app-image'));
    await user.type(screen.getByTestId('app-image'), 'nginx:1.28');
    expect(screen.getByTestId('app-name')).toHaveValue('web');
    await user.click(screen.getByTestId('app-submit'));
    expect(onSubmit).toHaveBeenCalledWith({
      input: expect.objectContaining({
        image: 'nginx:1.28',
        port: 3000,
        replicas: 2,
        healthCheckPath: '/healthz',
      }),
      secrets: [],
    });
  });

  test('shows the server refusal next to the form', () => {
    renderForm({ serverError: 'project already has an app' });
    expect(screen.getByRole('alert')).toHaveTextContent('project already has an app');
  });
});
