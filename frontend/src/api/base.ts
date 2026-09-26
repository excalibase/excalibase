// The control-plane API origin. Kept apart from the axios client so plain
// links (the provider sign-in buttons) can use it too.
export const API_BASE: string = import.meta.env.VITE_API_URL || 'http://localhost:24005/api';
