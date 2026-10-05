import { describe, test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthLayout } from './AuthLayout';

describe('AuthLayout', () => {
  test('frames the auth pages with the brand logo and the current tagline', () => {
    const { container } = render(
      <MemoryRouter initialEntries={['/login']}>
        <Routes>
          <Route element={<AuthLayout />}>
            <Route path="/login" element={<div>form</div>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    expect(container.querySelector('img[src="/logo-icon.png"]')).not.toBeNull();
    expect(screen.getByText('Databases, APIs and app hosting')).toBeInTheDocument();
    expect(screen.queryByText('Database provisioning platform')).not.toBeInTheDocument();
  });
});
