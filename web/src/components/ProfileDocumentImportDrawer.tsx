// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileDocumentImportDrawer pastes a single vendor-neutral NL profile or an
// EdgeX DeviceProfile JSON. It first calls the dry-run preview endpoint so the
// operator sees the parsed point count and warnings before anything persists;
// a successful import invalidates the profile list.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Space,
  Typography,
} from "antd";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";

import { extractError } from "../api/client";
import {
  importProfileDocument,
  previewProfileDocument,
  type ProfileDocumentImportResult,
} from "../api/profiles";

// Props configures the import drawer.
interface Props {
  open: boolean;
  onClose: () => void;
  onImported: () => void;
}

const { Paragraph, Text } = Typography;

// ProfileDocumentImportDrawer renders the paste → preview → import flow.
export default function ProfileDocumentImportDrawer({ open, onClose, onImported }: Props) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [text, setText] = useState("");
  const [preview, setPreview] = useState<ProfileDocumentImportResult | null>(null);

  // Parse the pasted text locally first to avoid sending malformed JSON.
  const parseDocument = (): unknown | null => {
    try {
      return JSON.parse(text);
    } catch {
      message.error(t("pages.profiles.docImport.badJson"));
      return null;
    }
  };

  const previewMutation = useMutation({
    mutationFn: previewProfileDocument,
    onSuccess: setPreview,
    onError: (err) => message.error(extractError(err).message),
  });

  const importMutation = useMutation({
    mutationFn: importProfileDocument,
    onSuccess: (res) => {
      message.success(
        t("pages.profiles.docImport.imported", { count: res.point_count }),
      );
      setText("");
      setPreview(null);
      onImported();
      onClose();
    },
    onError: (err) => message.error(extractError(err).message),
  });

  const handlePreview = () => {
    const doc = parseDocument();
    if (doc !== null) previewMutation.mutate(doc);
  };

  const handleImport = () => {
    const doc = parseDocument();
    if (doc !== null) importMutation.mutate(doc);
  };

  const handleClose = () => {
    setText("");
    setPreview(null);
    onClose();
  };

  return (
    <Drawer
      title={t("pages.profiles.docImport.title")}
      open={open}
      onClose={handleClose}
      width={720}
      footer={
        <Space>
          <Button onClick={handleClose}>{t("common.cancel")}</Button>
          <Button
            onClick={handlePreview}
            loading={previewMutation.isPending}
            disabled={!text.trim()}
          >
            {t("pages.profiles.docImport.preview")}
          </Button>
          <Button
            type="primary"
            onClick={handleImport}
            loading={importMutation.isPending}
            disabled={!text.trim()}
          >
            {t("pages.profiles.docImport.import")}
          </Button>
        </Space>
      }
    >
      <Paragraph type="secondary" style={{ marginBottom: 8 }}>
        {t("pages.profiles.docImport.hint")}
      </Paragraph>
      <textarea
        aria-label="profile-document"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          setPreview(null);
        }}
        style={{
          width: "100%",
          minHeight: 220,
          fontFamily: "monospace",
          fontSize: 12,
          padding: 8,
          borderRadius: 6,
          border: "1px solid #d9d9d9",
          resize: "vertical",
        }}
        placeholder={'{\n  "name": "acme-hvac",\n  "protocol": "modbus",\n  "points": [ ... ]\n}'}
      />
      {preview && (
        <Alert
          style={{ marginTop: 12 }}
          type={preview.warnings.length ? "warning" : "success"}
          showIcon
          message={t("pages.profiles.docImport.pointCount", {
            count: preview.point_count,
          })}
          description={
            preview.warnings.length ? (
              <ul style={{ margin: 0, paddingLeft: 18 }}>
                {preview.warnings.map((w, i) => (
                  <li key={i}>
                    <Text>{w}</Text>
                  </li>
                ))}
              </ul>
            ) : undefined
          }
        />
      )}
    </Drawer>
  );
}
