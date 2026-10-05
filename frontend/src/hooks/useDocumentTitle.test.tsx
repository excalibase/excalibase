import { describe, test, expect } from 'vitest';
import html from '../../index.html?raw';
import { renderHook } from '@testing-library/react';
import { useDocumentTitle } from './useDocumentTitle';

describe('useDocumentTitle', () => {
  test('suffixes the page with the app name', () => {
    renderHook(() => useDocumentTitle('Login'));
    expect(document.title).toBe('Login · Excalibase Studio');
  });

  test('without a page it is the app name alone', () => {
    renderHook(() => useDocumentTitle());
    expect(document.title).toBe('Excalibase Studio');
  });

  test('follows the page when it changes', () => {
    const { rerender } = renderHook(({ page }) => useDocumentTitle(page), { initialProps: { page: 'a' } });
    rerender({ page: 'b' });
    expect(document.title).toBe('b · Excalibase Studio');
  });
});

describe('index.html', () => {

  test('carries the Studio identity, not the Vite template', () => {
    expect(html).not.toContain('vite.svg');
    expect(html).toContain('<title>Excalibase Studio</title>');
    expect(html).toContain('name="description"');
    expect(html).toContain('name="theme-color"');
  });
});
