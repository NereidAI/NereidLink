// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretDrawer creates or rotates one vault entry. The type picker lists
// the tenant's edge secret types plus an ad-hoc option: typed forms render
// from the descriptor schema, the ad-hoc form offers free key/value rows
// with a per-row secret flag. Values are write-only.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Select,
  Space,
  Switch,
  Typography,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useEffect, useMemo } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery } from "@tanstack/react-query";

import {
  listSecretTypes,
  type SecretType,
} from "../api/secretTypes";
import {
  putSecretDocument,
  type SecretMetadata,
} from "../api/secrets";
import SchemaFields from "./schema/SchemaFields";
import { extractFields } from "./schema/schemaFieldsUtils";
import { extractError } from "../api/client";

interface SecretDrawerProps {
  open: boolean;
  target?: SecretMetadata | null;
  onClose: () => void;
  onSaved: () => void;
}

// blankType is the ad-hoc option key.
const blankType = "__blank__";

// blankRowsError is the sentinel thrown when the ad-hoc form is submitted
// without any key/value row, so onError can distinguish it from API errors.
const blankRowsError = new Error("blank rows required");

// SecretDrawer renders the create/rotate form.
export default function SecretDrawer({ open, target, onClose, onSaved }: SecretDrawerProps) {
  const { t } = useTranslation();
  const { message: toast } = AntApp.useApp();
  const [form] = Form.useForm();
  const { data: types = [] } = useQuery({
    queryKey: ["edge-secret-types"],
    queryFn: listSecretTypes,
    enabled: open,
  });

  const selectedType = Form.useWatch("type_ref", form) as string | undefined;

  const descriptor: SecretType | undefined = useMemo(() => {
    if (!selectedType || selectedType === blankType) return undefined;
    const [secretType, version] = selectedType.split("|");
    return types.find(
      (item) => item.secret_type === secretType && item.type_version === version,
    );
  }, [selectedType, types]);

  useEffect(() => {
    if (open) {
      form.resetFields();
      if (target && target.kind === "document") {
        form.setFieldsValue({
          name: target.name,
          type_ref:
            target.secret_type === ""
              ? blankType
              : `${target.secret_type}|${target.type_version}`,
        });
      }
    }
  }, [open, target, form]);

  const mutation = useMutation({
    mutationFn: async () => {
      const values = await form.validateFields();
      const isBlank = values.type_ref === blankType;
      let secretType = "";
      let version = "";
      if (!isBlank) {
        [secretType, version] = values.type_ref.split("|");
      }
      let document: Record<string, unknown>;
      if (isBlank) {
        document = {};
        for (const row of values.document?.rows ?? []) {
          if (row.key) document[row.key] = row.value;
        }
        // An empty ad-hoc document is rejected by the vault as an empty
        // value; fail on the client with the error next to the row editor,
        // not misattributed to the name field.
        if (Object.keys(document).length === 0) {
          throw blankRowsError;
        }
      } else {
        document = values.document ?? {};
      }
      return putSecretDocument(values.name, {
        secret_type: secretType,
        type_version: version,
        document,
      });
    },
    onSuccess: () => {
      onSaved();
      onClose();
    },
    onError: (error) => {
      if (error === blankRowsError) {
        form.setFields([
          {
            name: ["document", "rows"],
            errors: [t("pages.secrets.blank.atLeastOne")],
          },
        ]);
        return;
      }
      // Vault name errors attach to the name field; every other rejection
      // is shown as a toast so it is never mislabeled as an empty name.
      const text = extractError(error).message;
      if (/^name must|SECRET_INVALID_NAME/.test(text)) {
        form.setFields([{ name: "name", errors: [text] }]);
      } else {
        toast.error(text);
      }
    },
  });

  return (
    <Drawer
      title={target ? t("pages.secrets.titles.edit") : t("pages.secrets.titles.create")}
      width={480}
      open={open}
      onClose={onClose}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="primary" loading={mutation.isPending} onClick={() => mutation.mutate()}>
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical" initialValues={{ type_ref: blankType }}>
        <Form.Item
          name="name"
          label={t("pages.secrets.form.name")}
          rules={[{ required: true }]}
        >
          <Input disabled={!!target} />
        </Form.Item>
        <Form.Item
          name="type_ref"
          label={t("pages.secrets.form.type")}
          rules={[{ required: true }]}
        >
          <Select
            options={[
              { value: blankType, label: t("pages.secrets.blankType") },
              ...types.map((item) => ({
                value: `${item.secret_type}|${item.type_version}`,
                label: item.secret_type,
              })),
            ]}
          />
        </Form.Item>

        {descriptor ? (
          <TypedDocumentForm descriptor={descriptor} />
        ) : (
          <Form.List name={["document", "rows"]}>
            {(fields, { add, remove }) => (
              <>
                {fields.map((field) => (
                  <Space key={field.key} align="baseline">
                    <Form.Item
                      {...field}
                      name={[field.name, "key"]}
                      rules={[{ required: true }]}
                    >
                      <Input placeholder={t("pages.secrets.blank.key")} />
                    </Form.Item>
                    <Form.Item {...field} name={[field.name, "value"]}>
                      <Input placeholder={t("pages.secrets.blank.value")} />
                    </Form.Item>
                    <Form.Item {...field} name={[field.name, "secret"]} valuePropName="checked">
                      <Switch size="small" />
                    </Form.Item>
                    <Button type="text" danger icon={<DeleteOutlined />} onClick={() => remove(field.name)} />
                  </Space>
                ))}
                <Button type="dashed" icon={<PlusOutlined />} onClick={() => add({ secret: true })} block>
                  {t("pages.secrets.blank.add")}
                </Button>
              </>
            )}
          </Form.List>
        )}
      </Form>
    </Drawer>
  );
}

interface TypedDocumentFormProps {
  descriptor: SecretType;
}

// TypedDocumentForm renders the descriptor's fields nested under the
// form's "document" path via an explicit name prefix, so validated values
// are collected as values.document = {field: value}. A wrapping
// <Form.Item name="document"> would NOT pass that prefix down to fields
// declared with their own names (they would register at the form root).
function TypedDocumentForm({ descriptor }: TypedDocumentFormProps) {
  const fields = extractFields(descriptor.fields_schema);
  if (fields.length === 0) {
    return (
      <Typography.Text type="secondary">
        {descriptor.secret_type}
      </Typography.Text>
    );
  }
  return (
    <SchemaFields schema={descriptor.fields_schema} namePrefix={["document"]} />
  );
}
