// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BatchCommandDrawer collects a command name, shared parameters and either
// a group target or explicit device selector.

import { useEffect } from "react";
import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Space,
} from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { createBatchCommand, type BatchTask } from "../api/batchCommands";
import { listDeviceGroups } from "../api/ownership";
import { listDevices } from "../api/devices";
import { extractError } from "../api/client";

// DEFAULT_TIMEOUT_MS is the prefilled command timeout (30s), offered as a
// reference; operators can override it.
const DEFAULT_TIMEOUT_MS = 30000;

// BatchCommandForm is the drawer form shape.
interface BatchCommandForm {
  name: string;
  targetType: "group" | "devices";
  groupId?: string;
  deviceIds?: string[];
  paramsText?: string;
  timeoutMS?: number;
}

// BatchCommandDrawerProps controls drawer rendering.
interface BatchCommandDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (task: BatchTask) => void;
}

// BatchCommandDrawer renders the batch command request form.
export default function BatchCommandDrawer({
  open,
  onClose,
  onCreated,
}: BatchCommandDrawerProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [form] = Form.useForm<BatchCommandForm>();
  const targetType = Form.useWatch("targetType", form);

  const groupsQuery = useQuery({
    queryKey: ["device-groups"],
    queryFn: listDeviceGroups,
    enabled: open,
  });

  // The explicit-list target needs a populated device dropdown; fetch one
  // large page so operators select names instead of typing UUIDs blind.
  const devicesQuery = useQuery({
    queryKey: ["devices-all-for-batch"],
    queryFn: () => listDevices({ page: 1, page_size: 500 }),
    enabled: open,
  });

  useEffect(() => {
    if (open) {
      form.setFieldsValue({
        targetType: "group",
        paramsText: "",
        timeoutMS: DEFAULT_TIMEOUT_MS,
      });
    }
  }, [form, open]);

  const submit = async () => {
    const values = await form.validateFields();
    let params: Record<string, unknown> | undefined;
    if (values.paramsText?.trim()) {
      params = JSON.parse(values.paramsText) as Record<string, unknown>;
    }
    const task = await createBatchCommand({
      name: values.name,
      params,
      timeout_ms: values.timeoutMS,
      group_id: values.targetType === "group" ? values.groupId : undefined,
      device_ids: values.targetType === "devices" ? values.deviceIds : undefined,
    });
    message.success(t("pages.commandCenter.batchCreated"));
    onCreated(task);
  };

  return (
    <Drawer
      open={open}
      title={t("pages.commandCenter.batchTitle")}
      onClose={onClose}
      width={720}
      destroyOnHidden
      footer={
        <Space style={{ marginLeft: "auto" }}>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            onClick={() => {
              submit().catch((error: unknown) => {
                if (error instanceof SyntaxError) {
                  message.error(t("pages.commandCenter.invalidParams"));
                  return;
                }
                message.error(extractError(error).message);
              });
            }}
          >
            {t("common.send")}
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical" initialValues={{ targetType: "group" }}>
        <Form.Item
          name="name"
          label={t("common.command")}
          rules={[{ required: true, message: t("pages.commandCenter.batchNameRequired") }]}
        >
          <Input />
        </Form.Item>
        <Form.Item name="targetType" label={t("pages.commandCenter.target")}>
          <Radio.Group>
            <Radio value="group">{t("pages.commandCenter.targetGroup")}</Radio>
            <Radio value="devices">{t("pages.commandCenter.targetDevices")}</Radio>
          </Radio.Group>
        </Form.Item>
        {targetType === "group" ? (
          <Form.Item
            name="groupId"
            label={t("pages.commandCenter.targetGroup")}
            rules={[{ required: true, message: t("pages.commandCenter.groupRequired") }]}
          >
            <Select
              loading={groupsQuery.isLoading}
              options={(groupsQuery.data ?? []).map((group) => ({
                value: group.id,
                label: group.name,
              }))}
              showSearch
              optionFilterProp="label"
            />
          </Form.Item>
        ) : (
          <Form.Item
            name="deviceIds"
            label={t("pages.commandCenter.targetDevices")}
            rules={[{ required: true, message: t("pages.commandCenter.devicesRequired") }]}
          >
          <Select
            mode="multiple"
            loading={devicesQuery.isLoading}
            options={(devicesQuery.data?.items ?? []).map((device) => ({
              value: device.id,
              label: device.alias ?? device.canonical_name,
            }))}
            showSearch
            optionFilterProp="label"
          />
          </Form.Item>
        )}
        <Form.Item name="paramsText" label={t("pages.commandCenter.params")}>
          <Input.TextArea rows={5} spellCheck={false} />
        </Form.Item>
        <Form.Item name="timeoutMS" label={t("pages.commandCenter.timeoutMS")}>
          <InputNumber min={1} style={{ width: "100%" }} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}
