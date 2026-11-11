// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useDebounce delays propagation of a changing value, used for the 300ms
// search input debounce required by the list-page conventions.

import { useEffect, useState } from "react";

export function useDebounce<T>(value: T, delay: number): T {
  const [debounced, setDebounced] = useState<T>(value);

  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);

  return debounced;
}
