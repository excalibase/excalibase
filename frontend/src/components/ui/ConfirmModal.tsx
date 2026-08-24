import { useState } from 'react';
import { AlertTriangle, X } from 'lucide-react';

interface ConfirmModalProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly onConfirm: () => void;
  readonly title: string;
  readonly message: string;
  readonly confirmLabel?: string;
  readonly confirmText?: string; // If set, user must type this to confirm (destructive)
  readonly destructive?: boolean;
  readonly loading?: boolean;
}

export function ConfirmModal({
  open,
  onClose,
  onConfirm,
  title,
  message,
  confirmLabel = 'Confirm',
  confirmText,
  destructive = false,
  loading = false,
}: ConfirmModalProps) {
  const [typed, setTyped] = useState('');

  if (!open) return null;

  const canConfirm = confirmText ? typed === confirmText : true;

  const handleConfirm = () => {
    if (!canConfirm || loading) return;
    onConfirm();
    setTyped('');
  };

  const handleClose = () => {
    setTyped('');
    onClose();
  };

  return (
    <>
      <button
        type="button"
        className="fixed inset-0 bg-black/50 z-50 cursor-default border-0 p-0"
        onClick={handleClose}
        aria-label="Close dialog"
        data-testid="modal-backdrop"
      />
      <div className="fixed inset-0 z-50 flex items-center justify-center p-4 pointer-events-none" data-testid="confirm-modal">
        <div
          className="bg-surface-card border border-border-primary rounded-xl shadow-2xl w-full max-w-md pointer-events-auto"
        >
          {/* Header */}
          <div className="flex items-center gap-3 px-6 pt-5 pb-0">
            {destructive && (
              <div className="p-2 rounded-full bg-red-500/10">
                <AlertTriangle className="w-5 h-5 text-red-400" />
              </div>
            )}
            <h3 className="text-lg font-semibold text-text-primary flex-1">{title}</h3>
            <button
              onClick={handleClose}
              className="p-1 rounded-lg text-text-tertiary hover:text-text-primary hover:bg-surface-hover"
              data-testid="modal-close"
            >
              <X className="w-5 h-5" />
            </button>
          </div>

          {/* Body */}
          <div className="px-6 py-4">
            <p className="text-sm text-text-secondary">{message}</p>
            {confirmText && (
              <div className="mt-4">
                <p className="text-xs text-text-tertiary mb-2">
                  Type <span className="font-mono font-bold text-text-primary">{confirmText}</span> to confirm
                </p>
                <input
                  type="text"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  className="w-full px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm focus:outline-none focus:ring-2 focus:ring-purple-500"
                  placeholder={confirmText}
                  data-testid="confirm-input"
                  autoFocus
                />
              </div>
            )}
          </div>

          {/* Footer */}
          <div className="flex justify-end gap-3 px-6 pb-5">
            <button
              onClick={handleClose}
              className="px-4 py-2 text-sm font-medium rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
              data-testid="modal-cancel"
            >
              Cancel
            </button>
            <button
              onClick={handleConfirm}
              disabled={!canConfirm || loading}
              className={`px-4 py-2 text-sm font-medium rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed ${
                destructive
                  ? 'bg-red-500 hover:bg-red-600 text-white'
                  : 'bg-purple-500 hover:bg-purple-600 text-white'
              }`}
              data-testid="modal-confirm"
            >
              {loading ? 'Processing...' : confirmLabel}
            </button>
          </div>
        </div>
      </div>
    </>
  );
}
