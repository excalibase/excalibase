# DataTable

Shared list table for Studio. Import from `components/ui/DataTable`.

```tsx
<DataTable
  label="Projects"                      // names the table, the search box and the empty text
  columns={[{ id: 'name', header: 'Name', cell: (r) => r.name, sortValue: (r) => r.name, text: (r) => r.name, locked: true }]}
  rows={rows}
  getRowId={(r) => r.id}
  isLoading={query.isLoading}
  error={query.error}                   // a 403 shows "no access", anything else offers onRetry
  onRetry={query.refetch}
  filters={[{ id: 'plan', label: 'Plan', options, matches: (row, value) => row.plan === value }]}
  paging={{ mode: 'client', pageSize: 25 }}
  storageKey="projects"                 // remembers hidden columns in localStorage (never throws)
  rowActions={(r) => <button type="button">Open</button>}
  selectable bulkActions={(selected, clear) => ...}
  toolbarActions={<button>New project</button>}
  emptyState={<p>No projects yet.</p>}
/>
```

Column: `id`, `header`, `cell`, optional `sortValue` (makes it sortable), `text` (what the search box matches),
`defaultHidden`, `locked` (cannot be hidden).

## Server mode

For lists too big to load whole, pass `rows` as the current page and:

```tsx
paging={{ mode: 'server', total, page, pageSize, onPageChange, onSortChange, onSearchChange, onFilterChange }}
```

The table then does no searching, filtering or sorting itself. It reports `onSortChange({ columnId, direction })`,
`onSearchChange(text)` and `onFilterChange({ [filterId]: value })`, and the caller refetches. A column still needs
`sortValue` to show a sort button in server mode (its result is not used). Changing search or a filter should send
the caller back to page 1; the table sends nothing else for that, so reset `page` in your own state.

The props are additive only: new optional props, no renames.
