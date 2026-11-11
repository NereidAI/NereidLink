// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CreateProfileDrawer collects only the minimum to create a profile (its name
// and protocol family). The heavy content — connector template and the point
// library — is maintained afterwards on the profile detail page, so creation
// stays a one-field decision instead of opening the full tabbed editor. On
// success the caller navigates to the new profile's detail route.

import {
  App as AntApp,
  AutoComplete,
  Button,
  Drawer,
  Form,
  Input,
  Space,
} from "antd";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { ProfileInput } from "../api/profiles";
import { createProfile } from "../api/profiles";
import {
  familyOptions,
  useProtocolFamilies,
} from "./connector/useProtocolCatalog";
import { extractError } from "../api/client";

interface CreateProfileDrawerProps {
  // open controls visibility.
  open: boolean;
  // onClose hides the drawer without creating.
  onClose: () => void;
  // onCreated hands the new profile id to the caller for navigation.
  onCreated: (id: string) => void;
}

interface CreateFormValues {
  name: string;
  protocol?: string;
}

// CreateProfileDrawer renders the lightweight creation drawer.
export default function CreateProfileDrawer({
  open,
  onClose,
  onCreated,
}: CreateProfileDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<CreateFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const familiesQuery = useProtocolFamilies();
  const protocolOptions = familyOptions(familiesQuery.data);

  const mutation = useMutation({
    mutationFn: async (values: CreateFormValues) => {
      const input: ProfileInput = {
        name: values.name,
        protocol: values.protocol?.trim() || undefined,
        descriptors: [],
      };
      return createProfile(input);
    },
    onSuccess: async (profile) => {
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(t("pages.profiles.toasts.created"));
      form.resetFields();
      onCreated(profile.id);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      title={t("pages.profiles.form.createTitle")}
      onClose={onClose}
      width={720}
      destroyOnHidden
      footer={
        <Space style={{ marginLeft: "auto" }}>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            onClick={() => form.submit()}
            loading={mutation.isPending}
          >
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{ name: "", protocol: "" }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.profiles.form.profileName")}
          name="name"
          rules={[{ required: true, message: t("pages.profiles.form.nameRequired") }]}
        >
          <Input placeholder={t("pages.profiles.form.namePlaceholder")} autoFocus />
        </Form.Item>
        <Form.Item
          label={t("pages.profiles.form.protocol")}
          name="protocol"
          extra={t("pages.profiles.form.protocolHint")}
        >
          <AutoComplete
            options={protocolOptions}
            placeholder={t("pages.profiles.form.protocolPlaceholder")}
            allowClear
            filterOption
          />
        </Form.Item>
      </Form>
    </Drawer>
  );
}
