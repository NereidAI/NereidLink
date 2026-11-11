// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretsPanel renders the per-tenant secret vault table inside the
// combined secrets page. Rows show name, kind, type and timestamps only;
// content is never fetched. Create/rotate opens SecretDrawer and deletion
// opens SecretDeleteModal.

import { Button, Space, Table, Tooltip } from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";

import FilterSearchBar from "./FilterSearchBar";
import SecretDrawer from "./SecretDrawer";
import SecretDeleteModal from "./SecretDeleteModal";
import { listSecrets, type SecretMetadata } from "../api/secrets";

// SecretsPanel renders the vault list and its drawers/modals.
export default function SecretsPanel() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [keyword, setKeyword] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [target, setTarget] = useState<SecretMetadata | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<SecretMetadata | null>(
    null,
  );

  const { data: items = [], isLoading } = useQuery({
    queryKey: ["secrets"],
    queryFn: listSecrets,
  });

  // Client-side keyword filter over the fields operators actually know:
  // name, type and kind.
  const filtered = items.filter((row) =>
    [row.name, row.secret_type, row.kind]
      .join(" ")
      .toLowerCase()
      .includes(keyword.trim().toLowerCase()),
  );

  const openCreate = () => {
    setTarget(null);
    setDrawerOpen(true);
  };

  const openRotate = (row: SecretMetadata) => {
    setTarget(row);
    setDrawerOpen(true);
  };

  const columns = [
    {
      title: t("pages.secrets.table.name"),
      dataIndex: "name",
    },
    {
      title: t("pages.secrets.table.kind"),
      dataIndex: "kind",
      width: 110,
      render: (kind: string) => t(`pages.secrets.kinds.${kind}`),
    },
    {
      title: t("pages.secrets.table.type"),
      dataIndex: "secret_type",
      width: 160,
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.secrets.table.updated"),
      dataIndex: "updated_at",
      width: 180,
    },
    {
      title: "",
      width: 110,
      render: (_: unknown, row: SecretMetadata) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button type="text" icon={<EditOutlined />} onClick={() => openRotate(row)} />
          </Tooltip>
          <Tooltip title={t("common.delete")}>
            <Button
              type="text"
              danger
              icon={<DeleteOutlined />}
              onClick={() => setDeleteTarget(row)}
            />
          </Tooltip>
        </Space>
      ),
    },
  ];

  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          {t("pages.secrets.add")}
        </Button>
      </Space>
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.secrets.searchPlaceholder")}
      />
      <Table
        rowKey="id"
        loading={isLoading}
        dataSource={filtered}
        columns={columns}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
        }}
      />
      <SecretDrawer
        open={drawerOpen}
        target={target}
        onClose={() => setDrawerOpen(false)}
        onSaved={() => queryClient.invalidateQueries({ queryKey: ["secrets"] })}
      />
      {deleteTarget && (
        <SecretDeleteModal
          secret={deleteTarget}
          onClose={() => setDeleteTarget(null)}
          onDeleted={() =>
            queryClient.invalidateQueries({ queryKey: ["secrets"] })
          }
        />
      )}
    </div>
  );
}
