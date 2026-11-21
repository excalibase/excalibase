import { describe, expect, test } from 'vitest';
import {
  INITIAL_LIST, PAGE_SIZE, PROJECT_SORTS, USER_SORTS, ORG_SORTS, listParams, withFilters, withPage, withSearch, withSort,
} from './listQuery';

describe('listParams', () => {
  test('first page of an untouched list asks for the page size and nothing else', () => {
    expect(listParams(INITIAL_LIST, PROJECT_SORTS)).toEqual({ limit: PAGE_SIZE, offset: 0 });
  });

  test('page n starts after the rows of the pages before it', () => {
    expect(listParams(withPage(INITIAL_LIST, 4), PROJECT_SORTS)).toMatchObject({ limit: PAGE_SIZE, offset: PAGE_SIZE * 3 });
  });

  test('search is trimmed and an empty search is left out', () => {
    expect(listParams(withSearch(INITIAL_LIST, '  acme '), PROJECT_SORTS).q).toBe('acme');
    expect(listParams(withSearch(INITIAL_LIST, '   '), PROJECT_SORTS)).not.toHaveProperty('q');
  });

  test('a sort on an allowed column carries its direction', () => {
    const state = withSort(INITIAL_LIST, { columnId: 'created', direction: 'desc' });
    expect(listParams(state, PROJECT_SORTS)).toMatchObject({ sort: 'created', order: 'desc' });
  });

  test('a sort on a column the server does not offer is dropped, never sent', () => {
    const state = withSort(INITIAL_LIST, { columnId: 'cpu', direction: 'asc' });
    expect(listParams(state, PROJECT_SORTS)).not.toHaveProperty('sort');
  });

  test('filters go out under their own names and a cleared one is gone', () => {
    const state = withFilters(INITIAL_LIST, { plan: 'FREE', owner: 'suspended' });
    expect(listParams(state, PROJECT_SORTS)).toMatchObject({ plan: 'FREE', owner: 'suspended' });
    expect(listParams(withFilters(state, { plan: 'FREE' }), PROJECT_SORTS)).not.toHaveProperty('owner');
  });
});

describe('moving around a list', () => {
  const onPageThree = withPage(INITIAL_LIST, 3);

  test('searching, filtering and sorting each send the reader back to page 1', () => {
    expect(withSearch(onPageThree, 'x').page).toBe(1);
    expect(withFilters(onPageThree, { plan: 'FREE' }).page).toBe(1);
    expect(withSort(onPageThree, { columnId: 'name', direction: 'asc' }).page).toBe(1);
  });

  test('changing the page keeps the search, filters and sort', () => {
    const state = withPage(withSort(withFilters(withSearch(INITIAL_LIST, 'a'), { plan: 'FREE' }), { columnId: 'name', direction: 'asc' }), 2);
    expect(state).toMatchObject({ page: 2, search: 'a', filters: { plan: 'FREE' }, sort: { columnId: 'name', direction: 'asc' } });
  });

  test('a state is never changed in place', () => {
    const before = structuredClone(INITIAL_LIST);
    withSearch(INITIAL_LIST, 'a');
    withPage(INITIAL_LIST, 2);
    expect(INITIAL_LIST).toEqual(before);
  });
});

test('the sort allow-lists match the columns the server accepts', () => {
  expect(PROJECT_SORTS).toEqual(['name', 'org', 'owner', 'plan', 'status', 'created']);
  expect(USER_SORTS).toEqual(['created', 'email', 'lastLogin', 'orgs', 'projects', 'apps']);
  expect(ORG_SORTS).toEqual(['created', 'name', 'owner', 'plan', 'projects', 'apps']);
});
