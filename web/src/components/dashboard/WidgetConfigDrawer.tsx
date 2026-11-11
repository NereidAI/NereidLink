// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// WidgetConfigDrawer edits the binding of one dashboard widget (ADR-0020):
// an optional title, the device, the measurement key and, for a
// timeseries widget, the history window. Device and key lists are fetched
// through the same API as other pages and the selects are searchable.

import { Button, Drawer, Form, Input, Select, Space } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { Widget } from "../../api/dashboards";
import {
  listDevices,
  listTelemetryKeys,
  type TelemetryRange,
} from "../../api/devices";

// rangeOptions are the windows offered to a timeseries widget.
const rangeOptions: TelemetryRange[] = ["1h", "6h", "24h", "7d"];

// WidgetConfigValues is the form shape.
interface WidgetConfigValues {
  title?: string;
  device_id?: string;
  measurement_key?: string;
  range?: TelemetryRange;
}

// WidgetConfigDrawer renders the binding editor for one widget.
export default function WidgetConfigDrawer({
  widget,
  onCancel,
  onSubmit,
}: {
  widget?: Widget;
  onCancel: () => void;
  onSubmit: (widget: Widget) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<WidgetConfigValues>();
  const deviceId = Form.useWatch("device_id", form);

  const devicesQuery = useQuery({
    queryKey: ["dash-config-devices"],
    queryFn: () =>
      listDevices({ state: "ACTIVE", page: 1, page_size: 500 }),
  });

  const keysQuery = useQuery({
    queryKey: ["dash-config-keys", deviceId],
    queryFn: () => listTelemetryKeys(deviceId ?? ""),
    enabled: !!deviceId,
  });

  const isAlarms = widget?.type === "alarms";

  const submit = (values: WidgetConfigValues) => {
    if (!widget) {
      return;
    }
    onSubmit({
      ...widget,
      title: values.title?.trim() ?? "",
      device_id: values.device_id ?? "",
      measurement_key: values.measurement_key ?? "",
      range: values.range ?? widget.range ?? "1h",
    });
  };

  return (
    <Drawer
      open={!!widget}
      title={t("pages.dashboards.configureTitle")}
      onClose={onCancel}
      width={720}
      destroyOnHidden
      footer={
        <Space style={{ marginLeft: "auto" }}>
          <Button onClick={onCancel}>{t("common.cancel")}</Button>
          <Button type="primary" onClick={() => form.submit()}>
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        onFinish={submit}
        preserve={false}
        initialValues={{
          title: widget?.title ?? "",
          device_id: widget?.device_id,
          measurement_key: widget?.measurement_key,
          range: (widget?.range || "1h") as TelemetryRange,
        }}
      >
        <Form.Item label={t("pages.dashboards.fields.title")} name="title">
          <Input />
        </Form.Item>
        <Form.Item label={t("pages.dashboards.fields.device")} name="device_id">
          <Select
            showSearch
            allowClear
            optionFilterProp="label"
            placeholder={t("pages.dashboards.fields.devicePlaceholder")}
            options={(devicesQuery.data?.items ?? []).map((device) => ({
              value: device.id,
              label: device.canonical_name,
            }))}
          />
        </Form.Item>
        {!isAlarms && (
          <Form.Item
            label={t("pages.dashboards.fields.key")}
            name="measurement_key"
            rules={[{ required: true }]}
          >
            <Select
              showSearch
              optionFilterProp="label"
              options={(keysQuery.data ?? []).map((key) => ({
                value: key.measurement_key,
                label: key.measurement_key,
              }))}
            />
          </Form.Item>
        )}
        {widget?.type === "timeseries" && (
          <Form.Item label={t("pages.dashboards.fields.range")} name="range">
            <Select
              options={rangeOptions.map((value) => ({ value, label: value }))}
            />
          </Form.Item>
        )}
      </Form>
    </Drawer>
  );
}
