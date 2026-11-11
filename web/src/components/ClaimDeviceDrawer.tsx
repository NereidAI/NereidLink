// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ClaimDeviceDrawer claims an unclaimed device: the operator pastes the
// single-use claim token and selects the destination customer/group.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Select,
  Space,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  claimDevice,
  listCustomers,
  listDeviceGroups,
  type Customer,
  type DeviceGroup,
} from "../api/ownership";
import type { Device } from "../api/devices";
import { extractError } from "../api/client";

interface ClaimDeviceDrawerProps {
  // open controls visibility.
  open: boolean;

  // device is the unclaimed target device.
  device?: Device;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

interface ClaimFormValues {
  token: string;
  customer_id?: string;
  group_id?: string;
}

// ClaimDeviceDrawer renders the claim form.
export default function ClaimDeviceDrawer({
  open,
  device,
  onClose,
}: ClaimDeviceDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<ClaimFormValues>();
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
    mutationFn: async (values: ClaimFormValues) => {
      if (!device) {
        return;
      }
      await claimDevice(device.id, {
        token: values.token,
        customer_id: values.customer_id,
        group_id: values.group_id,
      });
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["devices"] });
      message.success(t("pages.devices.toasts.claimed"));
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
      title={t("pages.devices.lifecycle.claimTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.devices.lifecycle.token")}
          name="token"
          rules={[{ required: true, message: t("pages.devices.lifecycle.tokenRequired") }]}
        >
          <Input.TextArea rows={3} />
        </Form.Item>
        <Form.Item label={t("pages.devices.form.customer")} name="customer_id">
          <Select
            allowClear
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
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              {t("pages.devices.lifecycle.claim")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}
