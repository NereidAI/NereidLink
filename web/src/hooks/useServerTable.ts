// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// useServerTable wires TanStack Query to AntD Table server-side pagination.
// It never polls: pages are refetched on invalidation, window focus and
// network reconnect only, per ADR-0030.

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";

import i18n from "../i18n";

// PageEnvelope is the server pagination shape.
export interface PageEnvelope<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// TablePagination is the AntD-compatible controlled pagination state.
export interface TablePagination {
  current: number;
  pageSize: number;
  total: number;
  showSizeChanger: boolean;
  showTotal: (total: number) => string;
}

// useServerTable fetches one page through fetcher, keyed by params.
export function useServerTable<T, P extends Record<string, unknown>>(
  queryKey: string,
  params: P,
  fetcher: (params: P & { page: number; page_size: number }) => Promise<
    PageEnvelope<T>
  >,
) {
  const [current, setCurrent] = useState(1);
  const [pageSize, setPageSize] = useState(10);

  const query = useQuery({
    queryKey: [queryKey, params, current, pageSize],
    queryFn: () => fetcher({ ...params, page: current, page_size: pageSize }),
  });

  const data = query.data;
  const pagination: TablePagination = {
    current: data?.page ?? current,
    pageSize: data?.page_size ?? pageSize,
    total: data?.total ?? 0,
    showSizeChanger: true,
    showTotal: (total) => i18n.t("common.totalItems", { total }),
  };

  // onChange updates page/size; AntD also calls it for client filters, which
  // the server tables do not use.
  const onChange = (next: { current?: number; pageSize?: number }) => {
    if (next.pageSize && next.pageSize !== pageSize) {
      setPageSize(next.pageSize);
      setCurrent(1);
      return;
    }
    if (next.current) {
      setCurrent(next.current);
    }
  };

  return {
    items: data?.items ?? [],
    loading: query.isFetching,
    pagination,
    onChange,
    refetch: query.refetch,
  };
}
