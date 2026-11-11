// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ExpansionDrawer presents the server's immutable declarative artifact list.
// The UI does not recalculate, reorder, or digest artifacts locally, so the
// preview remains the exact generation the deploy and rollback owners use.

import { Descriptions, Drawer, Table, Typography } from "antd";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import type { Expansion, ExpansionArtifact } from "../../api/business";

interface ExpansionDrawerProps {
  expansion?: Expansion;
  open: boolean;
  onClose: () => void;
}

// ExpansionDrawer renders generation metadata and owner-specific artifacts.
export default function ExpansionDrawer({
  expansion,
  open,
  onClose,
}: ExpansionDrawerProps) {
  const { t } = useTranslation();
  const columns: ColumnsType<ExpansionArtifact> = [
    { title: t("business.expansion.artifactKey"), dataIndex: "key", width: 240 },
    { title: t("business.expansion.kind"), dataIndex: "kind", width: 210 },
    { title: t("business.expansion.gateway"), dataIndex: "gateway_id", width: 190 },
    { title: t("business.expansion.device"), dataIndex: "device_id", width: 190 },
    {
      title: t("business.expansion.dependencies"),
      dataIndex: "depends_on",
      render: (dependencies: string[]) =>
        dependencies.length > 0
          ? dependencies.join(", ")
          : t("business.expansion.noDependencies"),
    },
  ];

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={1000}
      title={t("business.expansion.title")}
    >
      {expansion && (
        <>
          <Descriptions
            bordered
            size="small"
            column={{ xs: 1, md: 2 }}
            style={{ marginBottom: 20 }}
            items={[
              {
                key: "instance",
                label: t("business.expansion.instance"),
                children: expansion.instance_id,
              },
              {
                key: "generation",
                label: t("business.expansion.generation"),
                children: expansion.generation,
              },
              {
                key: "template",
                label: t("business.expansion.template"),
                children: `${expansion.template.id}@${expansion.template.version}`,
              },
              {
                key: "schema",
                label: t("business.expansion.schemaVersion"),
                children: expansion.schema_version,
              },
              {
                key: "digest",
                label: t("business.expansion.digest"),
                children: <Typography.Text code>{expansion.digest}</Typography.Text>,
                span: 2,
              },
            ]}
          />
          <Typography.Paragraph type="secondary">
            {t("business.expansion.description")}
          </Typography.Paragraph>
          <Table<ExpansionArtifact>
            rowKey="key"
            columns={columns}
            dataSource={expansion.artifacts}
            scroll={{ x: 900 }}
            pagination={false}
            locale={{ emptyText: t("business.expansion.empty") }}
            expandable={{
              expandedRowRender: (artifact) => (
                <pre
                  aria-label={t("business.expansion.specFor", { key: artifact.key })}
                  style={{ maxHeight: 360, overflow: "auto", margin: 0 }}
                >
                  {JSON.stringify(artifact.spec, null, 2)}
                </pre>
              ),
              expandRowByClick: true,
            }}
          />
        </>
      )}
    </Drawer>
  );
}
