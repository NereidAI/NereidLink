// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CustomerFormDrawer is the create/edit drawer for customers.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Space,
} from "antd";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  createCustomer,
  updateCustomer,
  type Customer,
} from "../api/ownership";
import { extractError } from "../api/client";

interface CustomerFormDrawerProps {
  // open controls visibility.
  open: boolean;

  // customer is present when editing.
  customer?: Customer;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

interface CustomerFormValues {
  name: string;
  contact_name?: string;
  contact_email?: string;
  external_id?: string;
}

// CustomerFormDrawer renders the customer form.
export default function CustomerFormDrawer({
  open,
  customer,
  onClose,
}: CustomerFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<CustomerFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const mutation = useMutation({
    mutationFn: async (values: CustomerFormValues) => {
      const input = {
        name: values.name,
        contact_name: values.contact_name ?? "",
        contact_email: values.contact_email ?? "",
        external_id: values.external_id ?? "",
      };
      return customer
        ? updateCustomer(customer.id, input)
        : createCustomer(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["customers"] });
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
      title={customer
        ? t("pages.customers.customerForm.editTitle")
        : t("pages.customers.customerForm.createTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            name: customer?.name ?? "",
            contact_name: customer?.contact_name,
            contact_email: customer?.contact_email,
            external_id: customer?.external_id,
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
          label={t("pages.customers.fields.name")}
          name="name"
          rules={[{ required: true, message: t("pages.customers.fields.nameRequired") }]}
        >
          <Input />
        </Form.Item>
        <Form.Item label={t("pages.customers.fields.contactName")} name="contact_name">
          <Input />
        </Form.Item>
        <Form.Item label={t("pages.customers.fields.contactEmail")} name="contact_email">
          <Input />
        </Form.Item>
        <Form.Item label={t("pages.customers.fields.externalId")} name="external_id">
          <Input />
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
