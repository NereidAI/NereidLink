// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretDeleteModal guides vault secret deletion with deletion protection
// (ADR-0034, ticket 07): confirm a normal delete; if gateways still hold a
// copy the modal lists them and offers a forced delete that withdraws
// every binding behind a fresh TOTP step-up, deleting the vault original
// after all bindings are terminal.

import { Alert, Button, Modal, Space, Table, Tag, Typography } from "antd";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";

import {
  deleteSecret,
  type SecretMetadata,
  type SecretReference,
} from "../api/secrets";
import StepUpCard, { type StepUpLabels } from "./StepUpCard";
import { extractError } from "../api/client";

interface SecretDeleteModalProps {
  // secret is the vault entry being deleted.
  secret: SecretMetadata;
  // onClose dismisses the modal.
  onClose: () => void;
  // onDeleted refreshes state after a terminal/in-progress deletion.
  onDeleted: () => void;
}

// stage is the modal flow position.
type stage = "confirm" | "blocked" | "force"

// referenceStatusColor maps a binding status to a tag color.
function referenceStatusColor(status: string): string {
  switch (status) {
    case "APPLIED":
      return "success";
    case "FAILED":
      return "error";
    case "REMOVING":
      return "orange";
    default:
      return "default";
  }
}

// SecretDeleteModal renders the guarded deletion flow.
export default function SecretDeleteModal({
  secret,
  onClose,
  onDeleted,
}: SecretDeleteModalProps) {
  const { t } = useTranslation();
  const [stage, setStage] = useState<stage>("confirm");
  const [references, setReferences] = useState<SecretReference[]>([]);
  const [stepUpId, setStepUpId] = useState("");
  const [done, setDone] = useState<"deleted" | "deleting" | "">("");
  const [error, setError] = useState("");

  const normalDelete = useMutation({
    mutationFn: () => deleteSecret(secret.name),
    onSuccess: (result) => {
      if (result.outcome === "blocked") {
        setReferences(result.references);
        setStage("blocked");
        return;
      }
      setDone("deleted");
      onDeleted();
    },
    onError: (err) => setError(extractError(err).message),
  });

  const forceDelete = useMutation({
    mutationFn: () =>
      deleteSecret(secret.name, { force: true, step_up_id: stepUpId }),
    onSuccess: (result) => {
      if (result.outcome === "deleting") {
        setDone("deleting");
        onDeleted();
        return;
      }
      setDone("deleted");
      onDeleted();
    },
    onError: (err) => setError(extractError(err).message),
  });

  const stepUpLabels: StepUpLabels = {
    start: t("pages.secrets.delete.stepUp.start"),
    codePlaceholder: t("pages.secrets.delete.stepUp.codePlaceholder"),
    verify: t("pages.secrets.delete.stepUp.verify"),
    verified: t("pages.secrets.delete.stepUp.verified"),
  };

  const referenceColumns = [
    {
      title: t("pages.secrets.delete.columns.gateway"),
      dataIndex: "gateway_name",
    },
    {
      title: t("pages.secrets.delete.columns.status"),
      dataIndex: "status",
      render: (status: string) => (
        <Tag color={referenceStatusColor(status)}>
          {t(`pages.secrets.delete.refStatus.${status.toLowerCase()}`, {
            defaultValue: status,
          })}
        </Tag>
      ),
    },
  ];

  return (
    <Modal
      open
      width={620}
      title={t("pages.secrets.delete.title", { name: secret.name })}
      onCancel={onClose}
      footer={
        done ? (
          <Button onClick={onClose}>{t("common.close")}</Button>
        ) : (
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            {stage === "confirm" && (
              <Button
                type="primary"
                danger
                loading={normalDelete.isPending}
                onClick={() => normalDelete.mutate()}
              >
                {t("common.delete")}
              </Button>
            )}
            {stage === "blocked" && (
              <Button type="primary" onClick={() => setStage("force")}>
                {t("pages.secrets.delete.force")}
              </Button>
            )}
            {stage === "force" && (
              <Button
                type="primary"
                danger
                disabled={stepUpId === ""}
                loading={forceDelete.isPending}
                onClick={() => forceDelete.mutate()}
              >
                {t("pages.secrets.delete.forceConfirm")}
              </Button>
            )}
          </Space>
        )
      }
    >
      {error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 12 }}
          message={error}
          closable
          onClose={() => setError("")}
        />
      )}

      {done === "deleted" && (
        <Alert
          type="success"
          showIcon
          message={t("pages.secrets.delete.deleted")}
        />
      )}
      {done === "deleting" && (
        <Alert
          type="warning"
          showIcon
          message={t("pages.secrets.delete.deleting")}
        />
      )}

      {!done && stage === "confirm" && (
        <Typography.Paragraph>
          {t("pages.secrets.delete.confirm", { name: secret.name })}
        </Typography.Paragraph>
      )}

      {!done && (stage === "blocked" || stage === "force") && (
        <Space direction="vertical" style={{ width: "100%" }} size="middle">
          <Alert
            type="warning"
            showIcon
            message={t("pages.secrets.delete.inUse")}
          />
          <Table
            rowKey="gateway_id"
            size="small"
            dataSource={references}
            columns={referenceColumns}
            pagination={false}
          />
          {stage === "force" && (
            <StepUpCard
              autoStart
              action="edge_secret:force-delete"
              details={{ secret_name: secret.name }}
              labels={stepUpLabels}
              onVerified={setStepUpId}
            />
          )}
        </Space>
      )}
    </Modal>
  );
}
