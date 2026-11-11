// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// TransferDeviceDrawer moves an active device to another customer/group
// with credential reset and alarm policy choices.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Radio,
  Select,
  Space,
  Switch,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  listCustomers,
  listDeviceGroups,
  transferDevice,
  type Customer,
  type DeviceGroup,
} from "../api/ownership";
import type { Device } from "../api/devices";
import { extractError } from "../api/client";

interface TransferDeviceDrawerProps {
  // open controls visibility.
  open: boolean;

  // device is the target device.
  device?: Device;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

interface TransferFormValues {
  customer_id?: string;
  group_id?: string;
  reset_credentials: boolean;
  alarm_policy: "retain" | "clear";
}

// TransferDeviceDrawer renders the transfer form.
export default function TransferDeviceDrawer({
  open,
  device,
  onClose,
}: TransferDeviceDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<TransferFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const customersQuery = useQuery({ queryKey: ["customers"], queryFn: listCustomers });
  const groupsQuery = useQuery({ queryKey: ["device-groups"], queryFn: listDeviceGroups });

  const customerID = Form.useWatch("customer_id", form);

  const groupOptions = (groupsQuery.data ?? [])
    .filter((group: DeviceGroup) =>
      !customerID || !group.customer_id || group.customer_id === customerID,
    )
    .map((group: DeviceGroup) => ({
      value: group.id,
      label: group.name,
    }));

  const mutation = useMutation({
    mutationFn: async (values: TransferFormValues) => {
      if (!device) {
        return;
      }
      await transferDevice(device.id, {
        customer_id: values.customer_id ?? "",
        group_id: values.group_id,
        reset_credentials: values.reset_credentials,
        alarm_policy: values.alarm_policy,
      });
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["devices"] });
      message.success(t("pages.devices.toasts.transferred"));
      onClose();
    },
    onError: (error) => {
      message.error(extractError(error).message);
    },
  });

  return (
    <Drawer
      open={open}
      width={720}
      title={t("pages.devices.lifecycle.transferTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            customer_id: device?.customer_id,
            group_id: device?.group_id,
            reset_credentials: true,
            alarm_policy: "retain",
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
          label={t("pages.devices.form.customer")}
          name="customer_id"
          rules={[{ required: true, message: t("pages.devices.lifecycle.customerRequired") }]}
        >
          <Select
            placeholder={t("pages.devices.form.selectCustomer")}
            options={(customersQuery.data ?? []).map((customer: Customer) => ({
              value: customer.id,
              label: customer.name,
            }))}
          />
        </Form.Item>
        <Form.Item label={t("pages.devices.form.group")} name="group_id">
          <Select
            allowClear
            placeholder={t("pages.devices.form.selectGroup")}
            options={groupOptions}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.lifecycle.resetCredentials")}
          name="reset_credentials"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>
        <Form.Item label={t("pages.devices.lifecycle.alarmPolicy")} name="alarm_policy">
          <Radio.Group>
            <Radio value="retain">{t("pages.devices.lifecycle.alarmRetain")}</Radio>
            <Radio value="clear">{t("pages.devices.lifecycle.alarmClear")}</Radio>
          </Radio.Group>
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              {t("pages.devices.lifecycle.transfer")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}
