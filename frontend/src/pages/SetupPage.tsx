import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useForm } from '@tanstack/react-form';
import { Lock, Loader2, Copy, Check, AlertTriangle, KeyRound, UserPlus } from 'lucide-react';
import { useVaultStatus, useInitVault, useUnsealVault } from '../hooks/useVault';
import { useSetupStatus, useRegisterAdmin } from '../hooks/useSetup';
import { useAuthStore } from '../stores/auth-store';

type Step = 'init' | 'shares' | 'unseal' | 'admin' | 'done';

// validateAdminPassword replaces the nested ternary used inside the form's
// `onChange` validator (S3358) with explicit checks.
function validateAdminPassword(value: string): string | undefined {
  if (value.length < 8) return 'Min 8 characters';
  const hasMixedCase = /[A-Z]/.test(value) && /[a-z]/.test(value);
  if (!hasMixedCase || !/\d/.test(value)) return 'Mixed case + at least one digit';
  return undefined;
}

interface IssuedKeys {
  shares: string[];
  threshold: number;
}

const CLIPBOARD_CLEAR_MS = 30_000;

export function SetupPage() {
  const navigate = useNavigate();
  const { data: vaultStatus, isLoading: vaultLoading } = useVaultStatus();
  const { data: setupStatus, isLoading: setupLoading } = useSetupStatus();
  const initMutation = useInitVault();
  const unsealMutation = useUnsealVault();
  const registerMutation = useRegisterAdmin();
  const setAuth = useAuthStore((s) => s.setAuth);

  const [issuedKeys, setIssuedKeys] = useState<IssuedKeys | null>(null);
  const [copiedShare, setCopiedShare] = useState<string | null>(null);
  const [confirmedSaved, setConfirmedSaved] = useState(false);
  const [shareInput, setShareInput] = useState('');
  const [unsealError, setUnsealError] = useState<string | null>(null);

  const initForm = useForm({
    defaultValues: { shares: '5', threshold: '3' },
    onSubmit: async ({ value }) => {
      const sharesNum = Number(value.shares);
      const thresholdNum = Number(value.threshold);
      const result = await initMutation.mutateAsync({ shares: sharesNum, threshold: thresholdNum });
      setIssuedKeys({ shares: result.shares, threshold: result.threshold });
    },
  });

  const adminForm = useForm({
    defaultValues: { username: '', email: '', password: '', setupToken: '' },
    onSubmit: async ({ value }) => {
      const data = await registerMutation.mutateAsync(value);
      // Server set the httpOnly session cookie on the response — frontend
      // doesn't need to store the raw token. Pass it as legacyToken so the
      // axios header fallback still works for any code path that hasn't
      // moved to cookie auth yet.
      setAuth(
        {
          id: data.user.id,
          username: data.user.username,
          email: data.user.email,
          role: data.user.role,
        },
        { legacyToken: data.token },
      );
      navigate('/', { replace: true });
    },
  });

  if (vaultLoading || setupLoading || !vaultStatus || !setupStatus) {
    return (
      <div className="flex justify-center py-8">
        <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
      </div>
    );
  }

  // Order: vault init → shares display → vault unseal → admin user → done.
  // The shares step is sticky once issuedKeys is set so the operator can
  // confirm they saved the shares — even though the backend reports
  // initialized immediately after init returns. Only when the user clicks
  // Continue (which clears issuedKeys) does the wizard advance to unseal.
  function resolveStep(): Step {
    // Register the admin first: vault init/unseal/rekey now require an operator
    // credential (SEC-C1), and registering the first admin (auto-promoted) mints
    // the PAT the api client then sends on those calls. In auto-unseal
    // deployments the vault is already initialized, so we go straight to done.
    if (!setupStatus?.hasAdmin) return 'admin';
    if (issuedKeys) return 'shares';
    if (!vaultStatus?.initialized) return 'init';
    if (vaultStatus.sealed) return 'unseal';
    return 'done';
  }
  const step: Step = resolveStep();

  if (step === 'init') {
    return (
      <div data-testid="vault-setup-init">
        <Header
          icon={<KeyRound className="w-5 h-5 text-purple-400" />}
          title="Initialize Vault"
          subtitle="First-run secret store setup"
        />
        <p className="text-sm text-text-secondary mb-6">
          The vault stores all platform secrets (database credentials, signing keys). It is
          encrypted with a master key split via Shamir&apos;s Secret Sharing.
        </p>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            e.stopPropagation();
            void initForm.handleSubmit();
          }}
        >
          <div className="grid grid-cols-2 gap-3 mb-4">
            <initForm.Field
              name="shares"
              validators={{
                onChange: ({ value }) => validateInteger(value, 1, 10, 'Total shares'),
              }}
            >
              {(field) => (
                <NumericField label="Total shares (1–10)" field={field} testId="vault-init-shares" />
              )}
            </initForm.Field>

            <initForm.Field
              name="threshold"
              validators={{
                onChangeListenTo: ['shares'],
                onChange: ({ value, fieldApi }) => {
                  const shares = Number(fieldApi.form.getFieldValue('shares'));
                  const max = Number.isFinite(shares) && shares > 0 ? shares : 10;
                  return validateInteger(value, 1, max, 'Threshold');
                },
              }}
            >
              {(field) => (
                <NumericField
                  label="Threshold (≤ shares)"
                  field={field}
                  testId="vault-init-threshold"
                />
              )}
            </initForm.Field>
          </div>

          {initMutation.isError && (
            <ErrorBanner message={initMutation.error?.message ?? 'Init failed'} />
          )}

          <initForm.Subscribe selector={(s) => [s.canSubmit, s.isSubmitting] as const}>
            {([canSubmit, isSubmitting]) => (
              <button
                type="submit"
                disabled={!canSubmit || isSubmitting}
                className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center justify-center gap-2"
                data-testid="vault-init-submit"
              >
                {isSubmitting && <Loader2 className="w-4 h-4 animate-spin" />}
                Initialize Vault
              </button>
            )}
          </initForm.Subscribe>
        </form>
      </div>
    );
  }

  if (step === 'shares' && issuedKeys) {
    const copy = async (share: string) => {
      try {
        await navigator.clipboard.writeText(share);
        setCopiedShare(share);
        setTimeout(() => setCopiedShare((cur) => (cur === share ? null : cur)), 2_000);
        // Defence-in-depth: clear the clipboard after a window to reduce
        // share exposure if the user forgets to do so.
        setTimeout(() => {
          void navigator.clipboard.writeText('').catch(() => {});
        }, CLIPBOARD_CLEAR_MS);
      } catch {
        // Clipboard API unavailable (Firefox without HTTPS, denied perms);
        // fall back silently — operator can still triple-click and copy manually.
      }
    };

    return (
      <div data-testid="vault-setup-shares">
        <Header
          icon={<AlertTriangle className="w-5 h-5 text-amber-400" />}
          iconBg="amber"
          title="Save these shares"
          subtitle="Shown once — never again"
        />

        <div className="mb-4 px-3 py-2 rounded-lg bg-amber-500/10 border border-amber-500/30 text-xs text-amber-300">
          Distribute shares across separate trusted operators. Losing more than{' '}
          {issuedKeys.shares.length - issuedKeys.threshold} share(s) makes the vault unrecoverable.
          Clipboard auto-clears after 30 seconds.
        </div>

        <div className="space-y-2 mb-4">
          {issuedKeys.shares.map((share, idx) => (
            <div
              key={share}
              className="flex items-center gap-2 px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg"
            >
              <span className="text-xs font-medium text-text-secondary w-12">#{idx + 1}</span>
              <code className="flex-1 text-xs font-mono text-text-primary truncate">{share}</code>
              <button
                onClick={() => void copy(share)}
                className="p-1.5 rounded hover:bg-surface-hover text-text-secondary hover:text-text-primary transition-colors"
                data-testid={`vault-share-copy-${idx}`}
              >
                {copiedShare === share ? (
                  <Check className="w-3.5 h-3.5 text-green-400" />
                ) : (
                  <Copy className="w-3.5 h-3.5" />
                )}
              </button>
            </div>
          ))}
        </div>

        <label className="flex items-start gap-2 mb-4 cursor-pointer">
          <input
            type="checkbox"
            checked={confirmedSaved}
            onChange={(e) => setConfirmedSaved(e.target.checked)}
            className="mt-0.5"
            data-testid="vault-shares-confirm"
          />
          <span className="text-xs text-text-secondary">
            I have saved these shares securely. I understand they will not be shown again.
          </span>
        </label>

        <button
          onClick={() => {
            setIssuedKeys(null);
            setConfirmedSaved(false);
          }}
          disabled={!confirmedSaved}
          className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="vault-shares-continue"
        >
          Continue to unseal
        </button>
      </div>
    );
  }

  if (step === 'unseal') {
    return (
      <div data-testid="vault-setup-unseal">
        <Header
          icon={<Lock className="w-5 h-5 text-purple-400" />}
          title="Unseal Vault"
          subtitle={`${vaultStatus.progress} of ${vaultStatus.threshold} shares submitted`}
        />

        <div className="mb-4 h-1.5 rounded-full bg-bg-secondary overflow-hidden">
          <div
            className="h-full bg-purple-500 transition-all"
            style={{
              width: `${
                vaultStatus.threshold > 0 ? (vaultStatus.progress / vaultStatus.threshold) * 100 : 0
              }%`,
            }}
          />
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault();
            setUnsealError(null);
            unsealMutation.mutate(shareInput.trim(), {
              onSuccess: () => {
                setShareInput('');
              },
              onError: (err) => {
                setUnsealError(err.message);
              },
            });
          }}
        >
          <label className="block mb-3">
            <span className="text-xs font-medium text-text-secondary">Unseal share</span>
            <input
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={shareInput}
              onChange={(e) => setShareInput(e.target.value)}
              placeholder="Paste a share..."
              className="mt-1 w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm font-mono text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
              data-testid="vault-unseal-input"
              required
            />
          </label>

          {unsealError && <ErrorBanner message={unsealError} />}

          <button
            type="submit"
            disabled={unsealMutation.isPending || !shareInput.trim()}
            className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center justify-center gap-2"
            data-testid="vault-unseal-submit"
          >
            {unsealMutation.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
            Submit share
          </button>
        </form>
      </div>
    );
  }

  if (step === 'admin') {
    return (
      <div data-testid="vault-setup-admin">
        <Header
          icon={<UserPlus className="w-5 h-5 text-purple-400" />}
          title="Create platform admin"
          subtitle="First user becomes platform_admin"
        />

        <form
          onSubmit={(e) => {
            e.preventDefault();
            e.stopPropagation();
            void adminForm.handleSubmit();
          }}
        >
          <adminForm.Field
            name="username"
            validators={{
              onChange: ({ value }) =>
                value.trim().length < 3 ? 'Username must be at least 3 characters' : undefined,
            }}
          >
            {(field) => (
              <TextField
                label="Username"
                type="text"
                autoComplete="username"
                field={field}
                testId="admin-username"
              />
            )}
          </adminForm.Field>

          <adminForm.Field
            name="email"
            validators={{
              onChange: ({ value }) =>
                /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value) ? undefined : 'Enter a valid email',
            }}
          >
            {(field) => (
              <TextField
                label="Email"
                type="email"
                autoComplete="email"
                field={field}
                testId="admin-email"
              />
            )}
          </adminForm.Field>

          <adminForm.Field
            name="password"
            validators={{
              onChange: ({ value }) => validateAdminPassword(value),
            }}
          >
            {(field) => (
              <TextField
                label="Password"
                type="password"
                autoComplete="new-password"
                field={field}
                testId="admin-password"
                hint="Min 8 chars, mixed case, at least one digit"
              />
            )}
          </adminForm.Field>

          <adminForm.Field
            name="setupToken"
            validators={{
              onChange: ({ value }) => (value.trim().length === 0 ? 'Setup token is required' : undefined),
            }}
          >
            {(field) => (
              <TextField
                label="Setup token"
                type="password"
                autoComplete="off"
                field={field}
                testId="admin-setup-token"
                hint="printed in the server log on first start"
              />
            )}
          </adminForm.Field>

          {registerMutation.isError && (
            <ErrorBanner message={registerMutation.error?.message ?? 'Registration failed'} />
          )}

          <adminForm.Subscribe selector={(s) => [s.canSubmit, s.isSubmitting] as const}>
            {([canSubmit, isSubmitting]) => (
              <button
                type="submit"
                disabled={!canSubmit || isSubmitting}
                className="w-full px-4 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-50 text-white text-sm font-medium rounded-lg transition-colors flex items-center justify-center gap-2"
                data-testid="admin-submit"
              >
                {isSubmitting && <Loader2 className="w-4 h-4 animate-spin" />}
                Create admin & sign in
              </button>
            )}
          </adminForm.Subscribe>
        </form>
      </div>
    );
  }

  // step === 'done' — vault unsealed AND admin exists; guard redirects away
  return (
    <div className="flex justify-center py-8">
      <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
    </div>
  );
}

// ----------------------------------------------------------------------------
// Helpers — kept inside the file because they are wizard-specific styling.

function validateInteger(raw: string, min: number, max: number, label: string): string | undefined {
  const n = Number(raw);
  if (!Number.isInteger(n) || n < min || n > max) {
    return `${label} must be an integer between ${min} and ${max}`;
  }
  return undefined;
}

interface FieldApiLike<T> {
  state: { value: T; meta: { errors: unknown[]; isTouched: boolean } };
  handleChange: (value: T) => void;
  handleBlur: () => void;
}

interface NumericFieldProps {
  readonly label: string;
  readonly field: FieldApiLike<string>;
  readonly testId: string;
}

function NumericField({ label, field, testId }: NumericFieldProps) {
  const error = field.state.meta.isTouched ? (field.state.meta.errors[0] as string | undefined) : undefined;
  return (
    <label className="block">
      <span className="text-xs font-medium text-text-secondary">{label}</span>
      <input
        type="text"
        inputMode="numeric"
        pattern="[0-9]*"
        value={field.state.value}
        onChange={(e) => field.handleChange(e.target.value)}
        onBlur={field.handleBlur}
        className="mt-1 w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
        data-testid={testId}
      />
      {error && (
        <span className="text-[10px] text-red-400 mt-1 block" data-testid={`${testId}-error`}>
          {error}
        </span>
      )}
    </label>
  );
}

interface TextFieldProps {
  readonly label: string;
  readonly type: 'text' | 'email' | 'password';
  readonly autoComplete: string;
  readonly field: FieldApiLike<string>;
  readonly testId: string;
  readonly hint?: string;
}

function TextField({ label, type, autoComplete, field, testId, hint }: TextFieldProps) {
  const error = field.state.meta.isTouched ? (field.state.meta.errors[0] as string | undefined) : undefined;
  return (
    <label className="block mb-3">
      <span className="text-xs font-medium text-text-secondary">{label}</span>
      <input
        type={type}
        autoComplete={autoComplete}
        value={field.state.value}
        onChange={(e) => field.handleChange(e.target.value)}
        onBlur={field.handleBlur}
        className="mt-1 w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm text-text-primary focus:outline-none focus:ring-2 focus:ring-purple-500"
        data-testid={testId}
        required
      />
      {hint && !error && (
        <span className="text-[10px] text-text-tertiary mt-1 block">{hint}</span>
      )}
      {error && (
        <span className="text-[10px] text-red-400 mt-1 block" data-testid={`${testId}-error`}>
          {error}
        </span>
      )}
    </label>
  );
}

interface HeaderProps {
  readonly icon: React.ReactNode;
  readonly iconBg?: 'purple' | 'amber';
  readonly title: string;
  readonly subtitle: string;
}

function Header({ icon, iconBg = 'purple', title, subtitle }: HeaderProps) {
  const bg =
    iconBg === 'amber'
      ? 'bg-amber-500/10 border-amber-500/30'
      : 'bg-purple-500/10 border-purple-500/30';
  return (
    <div className="flex items-center gap-3 mb-4">
      <div className={`w-9 h-9 rounded-lg border flex items-center justify-center ${bg}`}>
        {icon}
      </div>
      <div>
        <h2 className="text-lg font-semibold text-text-primary">{title}</h2>
        <p className="text-xs text-text-secondary">{subtitle}</p>
      </div>
    </div>
  );
}

function ErrorBanner({ message }: { readonly message: string }) {
  return (
    <div className="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-xs text-red-400">
      {message}
    </div>
  );
}
