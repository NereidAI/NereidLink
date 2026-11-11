// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier-Identifier: Apache-2.0

// BulkImportDrawer is the shared validate → preview → confirm drawer for
// bulk imports (devices and profiles). The upload uses AntD's dragger so
// file picking and drag-and-drop are equally obvious. Once a file is
// chosen it is sent to the dry-run endpoint, which validates format and
// content without writing; the parsed rows are then shown with a
// per-row valid flag and the operator confirms before anything persists.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Space,
  Table,
  Tag,
  Upload,
} from "antd";
import {
  CheckCircleFilled,
  CloseCircleFilled,
  InboxOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import { extractError } from "../api/client";
import type { ImportPreview } from "../api/imports";

// BulkImportDrawerProps configures one import flow.
interface BulkImportDrawerProps {
  open: boolean;
  title: string;
  templateUrl: string;
  // accept is the input accept pattern, e.g. ".csv,.xlsx".
  accept: string;
  // formatHint names the accepted formats, e.g. "CSV or XLSX".
  formatHint: string;
  // preview validates the file without writing.
  preview: (file: File) => Promise<ImportPreview>;
  // doImport persists the chosen file.
  doImport: (file: File) => Promise<{ created: number; total: number }>;
  onImported: () => void;
  onClose: () => void;
}

// BulkImportDrawer renders the drag-and-drop import with preview.
export default function BulkImportDrawer({
  open,
  title,
  templateUrl,
  accept,
  formatHint,
  preview,
  doImport,
  onImported,
  onClose,
}: BulkImportDrawerProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [file, setFile] = useState<File | null>(null);
  const [data, setData] = useState<ImportPreview | null>(null);
  const [previewing, setPreviewing] = useState(false);
  const [importing, setImporting] = useState(false);

  // handleClose discards the in-progress flow before notifying the parent,
  // so reopening the drawer always starts at the upload step.
  const handleClose = () => {
    setFile(null);
    setData(null);
    setPreviewing(false);
    setImporting(false);
    onClose();
  };

  // chooseFile validates the extension client-side, then runs the
  // server-side dry-run so structural and content errors surface before
  // the operator can confirm.
  const chooseFile = async (picked: File) => {
    const allowed = accept.split(",").map((value) => value.trim().toLowerCase());
    const lower = picked.name.toLowerCase();
    if (!allowed.some((suffix) => lower.endsWith(suffix))) {
      setFile(null);
      setData(null);
      return;
    }
    setFile(picked);
    setData(null);
    setPreviewing(true);
    try {
      setData(await preview(picked));
    } catch (error) {
      message.error(extractError(error).message);
      setFile(null);
    } finally {
      setPreviewing(false);
    }
  };

  const confirm = async () => {
    if (!file) {
      return;
    }
    setImporting(true);
    try {
      const result = await doImport(file);
      message.success(t("components.bulkImport.done", { count: result.created }));
      onImported();
      onClose();
      return result;
    } catch (error) {
      message.error(extractError(error).message);
      // keep dialog open so the operator can fix and retry
    } finally {
      setImporting(false);
    }
  };

  const valueColumns = (data?.columns ?? []).map((column) => ({
    title: column,
    render: (_: unknown, row: ImportPreview["rows"][number]) =>
      row.values[column] || <span style={{ color: "#bbb" }}>-</span>,
  }));

  return (
    <Drawer
      open={open}
      title={title}
      width={860}
      destroyOnHidden
      onClose={handleClose}
      footer={
        <Space>
          <Button onClick={handleClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            loading={importing}
            disabled={!data || data.valid === 0}
            onClick={confirm}
          >
            {t("components.bulkImport.confirm", { count: data?.valid ?? 0 })}
          </Button>
        </Space>
      }
    >
      <Upload.Dragger
        accept={accept}
        multiple={false}
        showUploadList={false}
        beforeUpload={(picked) => {
          void chooseFile(picked);
          return false;
        }}
      >
        <p className="ant-upload-drag-icon">
          <InboxOutlined />
        </p>
        <p className="ant-upload-text">{t("components.bulkImport.dropOrClick")}</p>
        <p className="ant-upload-hint">
          {t("components.bulkImport.hint", { formats: formatHint })}
        </p>
      </Upload.Dragger>

      <Space style={{ marginTop: 12 }}>
        <Button type="link" onClick={() => window.open(templateUrl, "_blank")}>
          {t("components.bulkImport.downloadTemplate")}
        </Button>
        {file && <Tag>{file.name}</Tag>}
      </Space>

      {previewing && (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={t("components.bulkImport.validating")}
        />
      )}

      {data && (
        <>
          <Alert
            style={{ marginTop: 12 }}
            type={data.invalid === 0 ? "success" : "warning"}
            showIcon
            message={t("components.bulkImport.summary", {
              total: data.total,
              valid: data.valid,
              invalid: data.invalid,
            })}
            description={data.invalid > 0 ? t("components.bulkImport.partialHint") : undefined}
          />
          <Table
            style={{ marginTop: 12 }}
            rowKey="line"
            size="small"
            scroll={{ y: 280 }}
            pagination={false}
            dataSource={data.rows}
            columns={[
              { title: t("components.bulkImport.line"), dataIndex: "line", width: 64 },
              ...valueColumns,
              {
                title: t("components.bulkImport.status"),
                dataIndex: "valid",
                width: 110,
                render: (valid: boolean, row: ImportPreview["rows"][number]) =>
                  valid ? (
                    <Space>
                      <CheckCircleFilled style={{ color: "#52c41a" }} />
                      {t("components.bulkImport.valid")}
                    </Space>
                  ) : (
                    <Space>
                      <CloseCircleFilled style={{ color: "#ff4d4f" }} />
                      <span title={row.reason}>
                        {t("components.bulkImport.invalid")}
                      </span>
                    </Space>
                  ),
              },
            ]}
          />
          {data.rows.some((row) => !row.valid) && (
            <div style={{ marginTop: 8 }}>
              {data.rows
                .filter((row) => !row.valid)
                .map((row) => (
                  <div key={row.line} style={{ color: "#ff4d4f", fontSize: 12 }}>
                    {t("components.bulkImport.lineError", { line: row.line })} {row.reason}
                  </div>
                ))}
            </div>
          )}
        </>
      )}
    </Drawer>
  );
}
