// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// GroupFormDrawer is the create/edit drawer for device groups.

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
  createDeviceGroup,
  listCustomers,
  listDeviceGroups,
  updateDeviceGroup,
  type Customer,
  type DeviceGroup,
} from "../api/ownership";
import { extractError } from "../api/client";

interface GroupFormDrawerProps {
  // open controls visibility.
  open: boolean;

  // group is present when editing.
  group?: DeviceGroup;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

interface GroupFormValues {
  name: string;
  customer_id?: string;
  parent_id?: string;
}

// GroupFormDrawer renders the group form.
export default function GroupFormDrawer({
  open,
  group,
  onClose,
}: GroupFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<GroupFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const customersQuery = useQuery({ queryKey: ["customers"], queryFn: listCustomers });
  const groupsQuery = useQuery({ queryKey: ["device-groups"], queryFn: listDeviceGroups });

  const customerID = Form.useWatch("customer_id", form);

  const parentOptions = (groupsQuery.data ?? [])
    .filter((candidate) => candidate.id !== group?.id)
    .filter((candidate) =>
      !customerID || !candidate.customer_id || candidate.customer_id === customerID,
    )
    .map((candidate) => ({
      value: candidate.id,
      label: candidate.name,
    }));

  const mutation = useMutation({
    mutationFn: async (values: GroupFormValues) => {
      const input = {
        name: values.name,
        customer_id: values.customer_id ?? "",
        parent_id: values.parent_id ?? "",
      };
      if (group) {
        await updateDeviceGroup(group.id, input);
      } else {
        await createDeviceGroup(input);
      }
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["device-groups"] });
      message.success(t("pages.customers.toasts.saved"));
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
      title={group
        ? t("pages.customers.groupForm.editTitle")
        : t("pages.customers.groupForm.createTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            name: group?.name ?? "",
            customer_id: group?.customer_id,
            parent_id: group?.parent_id,
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
          label={t("pages.customers.fields.groupName")}
          name="name"
          rules={[{ required: true, message: t("pages.customers.fields.nameRequired") }]}
        >
          <Input />
        </Form.Item>
        <Form.Item label={t("pages.customers.fields.customer")} name="customer_id">
          <Select
            allowClear
            placeholder={t("pages.devices.form.selectCustomer")}
            options={(customersQuery.data ?? []).map((customer: Customer) => ({
              value: customer.id,
              label: customer.name,
            }))}
          />
        </Form.Item>
        <Form.Item label={t("pages.customers.fields.parent")} name="parent_id">
          <Select
            allowClear
            placeholder={t("pages.customers.fields.selectParent")}
            options={parentOptions}
          />
        </Form.Item>
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
