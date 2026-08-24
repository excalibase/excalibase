import { describe, test, expect, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useDarkMode } from './useDarkMode';

describe('useDarkMode', () => {
  beforeEach(() => {
    localStorage.clear();
    document.documentElement.classList.remove('dark');
  });

  test('defaults to dark when no preference saved', () => {
    const { result } = renderHook(() => useDarkMode());
    expect(typeof result.current.dark).toBe('boolean');
  });

  test('toggle flips the value and persists to localStorage', () => {
    const { result } = renderHook(() => useDarkMode());
    const before = result.current.dark;
    act(() => result.current.toggle());
    expect(result.current.dark).toBe(!before);
    expect(localStorage.getItem('theme-dark')).toBeTruthy();
  });

  test('reads from localStorage on init when set to false', () => {
    localStorage.setItem('theme-dark', 'false');
    const { result } = renderHook(() => useDarkMode());
    expect(result.current.dark).toBe(false);
  });
});
