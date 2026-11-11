// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StorageHookFormDrawer is the shared create/edit drawer for pre-storage
// hooks. Type-specific fields (script for expression, url for webhook) are
// shown conditionally; the config object is assembled on submit.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Upload,
} from "antd";
import { UploadOutlined } from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  createStorageHook,
  updateStorageHook,
  type StorageHook,
} from "../api/hooks";
import { extractError } from "../api/client";

interface StorageHookFormDrawerProps {
  // open controls visibility.
  open: boolean;

  // hook is present when editing.
  hook?: StorageHook;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

// FormValues mirrors the API shape except config, which is assembled from
// the flat script/url fields.
interface FormValues {
  name: string;
  enabled: boolean;
  order: number;
  hook_type: string;
  keysText: string;
  script: string;
  url: string;
  moduleB64: string;
  timeout_ms: number;
  failure_policy: string;
}

// maxWasmModuleMB mirrors maxWasmModuleBytes on the server (2 MiB).
const maxWasmModuleMB = 2;

// hookTypeOptions are the hook mechanisms.
const hookTypeOptions = [
  { value: "expression", label: "expression" },
  { value: "webhook", label: "webhook" },
  { value: "wasm", label: "wasm" },
];

// policyOptions are the failure policies from ADR-0015.
const policyOptions = [
  { value: "passthrough", label: "passthrough" },
  { value: "abort", label: "abort" },
  { value: "dlq", label: "dlq" },
];

// StorageHookFormDrawer renders the hook editor.
export default function StorageHookFormDrawer({
  open,
  hook,
  onClose,
}: StorageHookFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const hookTypeLabel: Record<string, string> = {
    expression: t("enums.expression"),
    webhook: t("enums.webhook"),
    wasm: t("enums.wasm"),
  };

  const policyLabel: Record<string, string> = {
    passthrough: t("enums.passthrough"),
    abort: t("enums.abort"),
    dlq: t("enums.dlq"),
  };

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      const measurementKeys = values.keysText
        .split(",")
        .map((key) => key.trim())
        .filter(Boolean);
      let config: Record<string, unknown>;
      if (values.hook_type === "expression") {
        config = { script: values.script };
      } else if (values.hook_type === "webhook") {
        config = { url: values.url };
      } else {
        // An empty module_b64 on update keeps the stored binary (the server
        // reuses it); on create it is rejected by the required rule/server.
        config = values.moduleB64 ? { module_b64: values.moduleB64 } : {};
      }
      const input = {
        name: values.name,
        enabled: values.enabled,
        order: values.order,
        hook_type: values.hook_type,
        measurement_keys: measurementKeys,
        config,
        timeout_ms: values.timeout_ms,
        failure_policy: values.failure_policy,
      };
      return hook
        ? updateStorageHook(hook.id, input)
        : createStorageHook(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["storage-hooks"] });
      message.success(
        hook
          ? t("pages.storageHooks.toasts.updated")
          : t("pages.storageHooks.toasts.created"),
      );
      onClose();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      width={720}
      title={hook ? t("pages.storageHooks.form.editTitle") : t("pages.storageHooks.form.createTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            name: hook?.name ?? "",
            enabled: hook?.enabled ?? true,
            order: hook?.order ?? 0,
            hook_type: hook?.hook_type ?? "expression",
            keysText: (hook?.measurement_keys ?? []).join(", "),
            script: hook?.config?.script ?? "",
            url: hook?.config?.url ?? "",
            // Editing shows module_size, never the binary; empty keeps it.
            moduleB64: "",
            timeout_ms: hook?.timeout_ms ?? 1000,
            failure_policy: hook?.failure_policy ?? "passthrough",
          });
        }
      }}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("common.name")}
          name="name"
          rules={[{ required: true, message: t("pages.storageHooks.form.nameRequired") }]}
        >
          <Input placeholder={t("pages.storageHooks.form.namePlaceholder")} />
        </Form.Item>

        <Space wrap>
          <Form.Item label={t("common.enabled")} name="enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item label={t("pages.storageHooks.form.order")} name="order">
            <InputNumber style={{ width: 100 }} />
          </Form.Item>
          <Form.Item label={t("pages.storageHooks.form.hookType")} name="hook_type">
            <Select
              style={{ width: 140 }}
              options={hookTypeOptions.map((option) => ({
                value: option.value,
                label: hookTypeLabel[option.value] ?? option.label,
              }))}
            />
          </Form.Item>
        </Space>

        <Form.Item
          label={t("pages.storageHooks.form.keysLabel")}
          name="keysText"
        >
          <Input placeholder={t("pages.storageHooks.form.keysPlaceholder")} />
        </Form.Item>

        <TypeFields storedModuleSize={hook?.config?.module_size} />

        <Space wrap>
          <Form.Item
            label={t("pages.storageHooks.form.timeout")}
            name="timeout_ms"
            rules={[{ required: true, message: t("pages.storageHooks.form.timeoutRequired") }]}
          >
            <InputNumber min={1} style={{ width: 130 }} />
          </Form.Item>
          <Form.Item label={t("pages.storageHooks.form.policy")} name="failure_policy">
            <Select
              style={{ width: 150 }}
              options={policyOptions.map((option) => ({
                value: option.value,
                label: policyLabel[option.value] ?? option.label,
              }))}
            />
          </Form.Item>
        </Space>

        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// TypeFields renders the type-specific script/url/module fields. It watches
// the hook type from the enclosing form instance.
function TypeFields({ storedModuleSize }: { storedModuleSize?: number }) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  // The watch hook must run inside a component rendered below the Form, so
  // useFormInstance provides the correct instance.
  const form = Form.useFormInstance();
  const hookType = Form.useWatch("hook_type", form);

  if (hookType === "webhook") {
    return (
      <Form.Item
        label={t("pages.storageHooks.form.webhookUrl")}
        name="url"
        rules={[{ required: true, message: t("pages.storageHooks.form.urlRequired") }]}
      >
        <Input placeholder={t("pages.storageHooks.form.urlPlaceholder")} />
      </Form.Item>
    );
  }

  if (hookType === "wasm") {
    return (
      <Form.Item
        label={t("pages.storageHooks.form.wasmModule")}
        required
        extra={
          storedModuleSize
            ? t("pages.storageHooks.form.wasmStored", { size: storedModuleSize })
            : undefined
        }
      >
        <Space direction="vertical" style={{ width: "100%" }}>
          <Upload
            accept=".wasm"
            maxCount={1}
            beforeUpload={(file) => {
              if (file.size > maxWasmModuleMB * 1024 * 1024) {
                message.error(
                  t("pages.storageHooks.form.wasmTooLarge", { max: maxWasmModuleMB }),
                );
                return Upload.LIST_IGNORE;
              }
              const reader = new FileReader();
              reader.onload = () => {
                const bytes = new Uint8Array(reader.result as ArrayBuffer);
                let binary = "";
                bytes.forEach((b) => {
                  binary += String.fromCharCode(b);
                });
                form.setFieldValue(
                  "moduleB64",
                  btoa(binary),
                );
                message.success(
                  t("pages.storageHooks.form.wasmSelected", { size: file.size }),
                );
              };
              reader.onerror = () =>
                message.error(t("pages.storageHooks.form.wasmBadFile"));
              reader.readAsArrayBuffer(file);
              // Keep the upload purely client-side; the base64 rides in JSON.
              return false;
            }}
          >
            <Button icon={<UploadOutlined />}>{t("pages.storageHooks.form.wasmUpload")}</Button>
          </Upload>
          <Form.Item
            name="moduleB64"
            noStyle
            rules={[
              {
                validator: (_, value: string) =>
                  !storedModuleSize && !value
                    ? Promise.reject(new Error(t("pages.storageHooks.form.wasmRequired")))
                    : Promise.resolve(),
              },
            ]}
          >
            <Input.TextArea
              rows={3}
              placeholder={t("pages.storageHooks.form.wasmPaste")}
            />
          </Form.Item>
        </Space>
      </Form.Item>
    );
  }

  return (
    <Form.Item
      label={t("pages.storageHooks.form.script")}
      name="script"
      rules={[{ required: true, message: t("pages.storageHooks.form.scriptRequired") }]}
    >
      <Input.TextArea rows={4} placeholder={t("pages.storageHooks.form.scriptPlaceholder")} />
    </Form.Item>
  );
}
