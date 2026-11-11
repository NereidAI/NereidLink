// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProbeCreateDrawer collects a gateway, a subset of its bound
// sub-devices, the probe mode and per-device timeout. Probe targets are
// restricted to sub-devices managed by one gateway because the gateway
// performs the actual field-bus reachability check.

import { useEffect } from "react";
import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  InputNumber,
  Radio,
  Select,
  Space,
  Tag,
} from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { createProbe, type ProbeTask } from "../api/probes";
import { listDevicesByGateway, listGatewayDevices } from "../api/devices";
import { extractError } from "../api/client";

// ProbeCreateForm is the drawer form shape.
interface ProbeCreateForm {
  gatewayId?: string;
  deviceIds?: string[];
  mode: "cached" | "fresh" | "strict";
  timeoutMS?: number;
}

// ProbeCreateDrawerProps controls drawer rendering.
interface ProbeCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  onCreated: (task: ProbeTask) => void;
}

// ProbeCreateDrawer renders the on-demand probe request form.
export default function ProbeCreateDrawer({
  open,
  onClose,
  onCreated,
}: ProbeCreateDrawerProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [form] = Form.useForm<ProbeCreateForm>();
  const gatewayId = Form.useWatch("gatewayId", form);

  const gatewaysQuery = useQuery({
    queryKey: ["gateway-devices"],
    queryFn: listGatewayDevices,
    enabled: open,
  });

  const devicesQuery = useQuery({
    queryKey: ["gateway-subdevices", gatewayId],
    queryFn: () => listDevicesByGateway(gatewayId as string),
    enabled: open && !!gatewayId,
  });

  useEffect(() => {
    if (open) {
      form.setFieldsValue({
        gatewayId: undefined,
        deviceIds: [],
        mode: "fresh",
        timeoutMS: 10000,
      });
    }
  }, [form, open]);

  // Clearing the gateway must also clear the selection, otherwise stale
  // device ids from another gateway could be submitted.
  useEffect(() => {
    if (!gatewayId) form.setFieldValue("deviceIds", []);
  }, [form, gatewayId]);

  const submit = async () => {
    const values = await form.validateFields();
    const task = await createProbe({
      devices: values.deviceIds ?? [],
      mode: values.mode,
      timeout_ms: values.timeoutMS,
    });
    message.success(t("pages.probes.created"));
    onCreated(task);
  };

  return (
    <Drawer
      open={open}
      title={t("pages.probes.createTitle")}
      onClose={onClose}
      width={640}
      destroyOnHidden
      footer={
        <Space style={{ marginLeft: "auto" }}>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            onClick={() => {
              submit().catch((error: unknown) => {
                message.error(extractError(error).message);
              });
            }}
          >
            {t("common.send")}
          </Button>
        </Space>
      }
    >
      <Form form={form} layout="vertical" initialValues={{ mode: "fresh" }}>
        <Form.Item
          name="gatewayId"
          label={t("pages.probes.gateway")}
          rules={[{ required: true, message: t("pages.probes.gatewayRequired") }]}
        >
          <Select
            loading={gatewaysQuery.isLoading}
            options={(gatewaysQuery.data ?? []).map((device) => ({
              value: device.id,
              label: (
                <Space>
                  {device.canonical_name}
                  {device.presence_state === "ONLINE" ? (
                    <Tag color="success">{t("status.online")}</Tag>
                  ) : (
                    <Tag>{t("status.offline")}</Tag>
                  )}
                </Space>
              ),
            }))}
            showSearch
            optionFilterProp="label"
            placeholder={t("pages.probes.gatewayPlaceholder")}
          />
        </Form.Item>
        <Form.Item
          name="deviceIds"
          label={t("pages.probes.devices")}
          rules={[{ required: true, message: t("pages.probes.devicesRequired") }]}
          extra={t("pages.probes.devicesHint")}
        >
          <Select
            mode="multiple"
            loading={devicesQuery.isLoading}
            disabled={!gatewayId}
            options={(devicesQuery.data ?? [])
              .filter((device) => device.state === "ACTIVE")
              .map((device) => ({
                value: device.id,
                label: (
                  <Space>
                    {device.canonical_name}
                    {device.presence_state === "ONLINE" ? (
                      <Tag color="success">{t("status.online")}</Tag>
                    ) : null}
                  </Space>
                ),
              }))}
            showSearch
            optionFilterProp="label"
            placeholder={t("pages.probes.devicesPlaceholder")}
            maxCount={100}
          />
        </Form.Item>
        <Form.Item name="mode" label={t("pages.probes.mode")}>
          <Radio.Group>
            <Radio value="cached">{t("pages.probes.modeCached")}</Radio>
            <Radio value="fresh">{t("pages.probes.modeFresh")}</Radio>
            <Radio value="strict">{t("pages.probes.modeStrict")}</Radio>
          </Radio.Group>
        </Form.Item>
        <Form.Item
          name="timeoutMS"
          label={t("pages.probes.timeoutMS")}
          rules={[{ required: true, message: t("pages.probes.timeoutRequired") }]}
        >
          <InputNumber min={1} max={60000} style={{ width: "100%" }} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}
