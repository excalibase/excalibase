import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useServerList } from './useServerList';
import { PAGE_SIZE, PROJECT_SORTS } from './listQuery';

describe('useServerList', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  test('typing asks the server once, after the reader stops', () => {
    const { result } = renderHook(() => useServerList(PROJECT_SORTS));
    const paging = () => result.current.paging(100);
    act(() => { if (paging().mode === 'server') (paging() as { onSearchChange?: (t: string) => void }).onSearchChange?.('a'); });
    act(() => { (paging() as { onSearchChange?: (t: string) => void }).onSearchChange?.('ac'); });
    expect(result.current.params).not.toHaveProperty('q');
    act(() => { vi.advanceTimersByTime(300); });
    expect(result.current.params.q).toBe('ac');
  });

  test('paging reports the page and total it was given', () => {
    const { result } = renderHook(() => useServerList(PROJECT_SORTS));
    const paging = result.current.paging(90);
    expect(paging).toMatchObject({ mode: 'server', total: 90, page: 1, pageSize: PAGE_SIZE });
    act(() => { (paging as { onPageChange: (p: number) => void }).onPageChange(3); });
    expect(result.current.params.offset).toBe(PAGE_SIZE * 2);
  });

  test('a filter change returns to page 1', () => {
    const { result } = renderHook(() => useServerList(PROJECT_SORTS));
    act(() => { (result.current.paging(90) as { onPageChange: (p: number) => void }).onPageChange(3); });
    act(() => { (result.current.paging(90) as { onFilterChange: (f: Record<string, string>) => void }).onFilterChange({ plan: 'FREE' }); });
    expect(result.current.params).toMatchObject({ plan: 'FREE', offset: 0 });
  });

  test('a sort change reaches the request', () => {
    const { result } = renderHook(() => useServerList(PROJECT_SORTS));
    act(() => { (result.current.paging(5) as { onSortChange: (s: { columnId: string; direction: 'asc' | 'desc' } | null) => void }).onSortChange({ columnId: 'name', direction: 'desc' }); });
    expect(result.current.params).toMatchObject({ sort: 'name', order: 'desc' });
  });
});
