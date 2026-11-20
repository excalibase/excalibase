import { useEffect, useState } from 'react';

// useDebounced follows value after it has stood still for delayMs, so a table
// searched while typing asks the server once, not once per key.
export function useDebounced<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), delayMs);
    return () => clearTimeout(timer);
  }, [value, delayMs]);
  return settled;
}
