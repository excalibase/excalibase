// The sentence that tells a customer how to add credit: there is no self-serve purchase or upgrade yet.
// The address is the server's own SUPPORT_EMAIL, so a self-hosted install names its own.
export const addCreditText = (supportEmail: string | undefined) =>
  supportEmail ? `Contact support to add credit: ${supportEmail}.` : 'Contact support to add credit.';
