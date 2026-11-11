// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceFormDrawer is the shared create/edit device drawer: vertical form,
// footer rendered inline, one mode for both actions per ADR-0030.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  Select,
  Space,
  Switch,
  Tooltip,
} from "antd";
import { useEffect } from "react";
import { ReloadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { Device, Profile } from "../api/devices";
import {
  createDevice,
  updateDevice,
  listGatewayDevices,
} from "../api/devices";
import { listGroups } from "../api/ruleChains";
import {
  listCustomers,
  listDeviceGroups,
  type Customer,
  type DeviceGroup,
} from "../api/ownership";
import { extractError } from "../api/client";
import { generateDeviceName } from "../utils/deviceName";

interface DeviceFormDrawerProps {
  // open controls visibility.
  open: boolean;

  // device is present when editing, absent when creating.
  device?: Device;

  // profiles are the selectable profiles.
  profiles: Profile[];

  // onClose hides the drawer without saving.
  onClose: () => void;
}

interface DeviceFormValues {
  canonical_name: string;
  profile_id?: string;
  rule_chain_group_id?: string;
  customer_id?: string;
  group_id?: string;
  unclaimed: boolean;
  is_gateway: boolean;
  gateway_id?: string;
  alias?: string;
  notes?: string;
}

// DeviceFormDrawer renders the create/edit form.
export default function DeviceFormDrawer({
  open,
  device,
  profiles,
  onClose,
}: DeviceFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<DeviceFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const groupsQuery = useQuery({
    queryKey: ["rule-chain-groups"],
    queryFn: listGroups,
  });
  const customersQuery = useQuery({
    queryKey: ["customers"],
    queryFn: listCustomers,
  });
  const deviceGroupsQuery = useQuery({
    queryKey: ["device-groups"],
    queryFn: listDeviceGroups,
  });
  const gatewaysQuery = useQuery({
    queryKey: ["gateway-devices"],
    queryFn: listGatewayDevices,
  });

  // The global staleTime keeps the gateway cache for a minute; a gateway
  // created moments earlier would otherwise be missing from "Managed by
  // gateway". Refetch whenever the drawer opens so the list is current.
  useEffect(() => {
    if (open) {
      gatewaysQuery.refetch();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const mutation = useMutation({
    mutationFn: async (values: DeviceFormValues) => {
      const input = {
        canonical_name: values.canonical_name,
        profile_id: values.profile_id || undefined,
        rule_chain_group_id: values.rule_chain_group_id || undefined,
        customer_id: values.unclaimed ? undefined : values.customer_id || undefined,
        group_id: values.unclaimed ? undefined : values.group_id || undefined,
        is_gateway: values.is_gateway,
        gateway_id: values.is_gateway ? undefined : values.gateway_id || undefined,
        state: values.unclaimed ? "UNCLAIMED" as const : "ACTIVE" as const,
        alias: values.alias?.trim() || undefined,
        notes: values.notes?.trim() || undefined,
      };
      return device ? updateDevice(device.id, input) : createDevice(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["devices"] });
      await queryClient.invalidateQueries({ queryKey: ["device"] });
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(
        device
          ? t("pages.devices.form.updated")
          : t("pages.devices.form.created"),
      );
      onClose();
    },
    onError: (error) => {
      const apiError = extractError(error);
      // A canonical-name collision is extremely unlikely with the generated
      // space, but the DB unique constraint is the final guard. Surface it
      // on the field and rotate the suggested name instead of toasting, so
      // the operator can simply save again.
      if (!device && apiError.status === 409) {
        const next = generateDeviceName(form.getFieldValue("is_gateway") === true);
        form.setFields([
          {
            name: "canonical_name",
            value: next,
            errors: [t("pages.devices.form.nameCollision")],
          },
        ]);
        return;
      }
      message.error(apiError.message);
    },
  });

  const customerID = Form.useWatch("customer_id", form);
  const unclaimed = Form.useWatch("unclaimed", form);
  const isGateway = Form.useWatch("is_gateway", form);

  const groupOptions = (deviceGroupsQuery.data ?? [])
    .filter((group: DeviceGroup) =>
      !customerID || !group.customer_id || group.customer_id === customerID,
    )
    .map((group: DeviceGroup) => ({
      value: group.id,
      label: group.name,
    }));

  return (
    <Drawer
      open={open}
      width={720}
      title={device ? t("pages.devices.form.editTitle") : t("pages.devices.form.createTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            // Console-created devices get a generated technical name;
            // editing devices show their immutable existing name.
            canonical_name: device?.canonical_name ?? generateDeviceName(false),
            profile_id: device?.profile_id,
            rule_chain_group_id: device?.rule_chain_group_id,
            customer_id: device?.customer_id,
            group_id: device?.group_id,
            unclaimed: !device ? false : device.state === "UNCLAIMED",
            is_gateway: device?.is_gateway ?? false,
            gateway_id: device?.gateway_id,
            alias: device?.alias ?? "",
            notes: device?.notes ?? "",
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
          label={t("pages.devices.form.canonicalName")}
          name="canonical_name"
          extra={device ? undefined : t("pages.devices.form.canonicalAutoHint")}
          rules={[
            { required: true, message: t("pages.devices.form.nameRequired") },
            { max: 255, message: t("pages.devices.form.maxLength") },
          ]}
        >
          <Input
            readOnly
            placeholder={t("pages.devices.form.namePlaceholder")}
            addonAfter={
              device ? null : (
                <Tooltip title={t("pages.devices.form.regenerateName")}>
                  <Button
                    type="text"
                    size="small"
                    icon={<ReloadOutlined />}
                    onClick={() =>
                      form.setFieldValue(
                        "canonical_name",
                        generateDeviceName(
                          form.getFieldValue("is_gateway") === true,
                        ),
                      )
                    }
                  />
                </Tooltip>
              )
            }
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.alias")}
          name="alias"
          rules={[{ max: 255, message: t("pages.devices.form.maxLength") }]}
        >
          <Input placeholder={t("pages.devices.form.aliasPlaceholder")} />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.unclaimed")}
          name="unclaimed"
          valuePropName="checked"
          extra={t("pages.devices.form.unclaimedHint")}
        >
          <Switch />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.gateway")}
          name="is_gateway"
          valuePropName="checked"
          extra={t("pages.devices.form.gatewayHint")}
        >
          <Switch
            onChange={(checked) => {
              // In create mode, keep the random tail but swap the prefix
              // so the generated name matches the chosen device type.
              if (device) return;
              const current = form.getFieldValue("canonical_name") as
                | string
                | undefined;
              const match = /^(?:dev|gw)-(.+)$/.exec(current ?? "");
              form.setFieldValue(
                "canonical_name",
                match ? `${checked ? "gw" : "dev"}-${match[1]}` : generateDeviceName(checked),
              );
            }}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.subdeviceGateway")}
          name="gateway_id"
          hidden={isGateway}
        >
          <Select
            allowClear
            showSearch
            placeholder={t("pages.devices.form.selectGateway")}
            optionFilterProp="label"
            options={(gatewaysQuery.data ?? []).map((gateway) => ({
              value: gateway.id,
              label: gateway.canonical_name,
            }))}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.customer")}
          name="customer_id"
          hidden={unclaimed}
        >
          <Select
            allowClear
            showSearch
            placeholder={t("pages.devices.form.selectCustomer")}
            optionFilterProp="label"
            options={(customersQuery.data ?? []).map((customer: Customer) => ({
              value: customer.id,
              label: customer.name,
            }))}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.group")}
          name="group_id"
          hidden={unclaimed}
        >
          <Select
            allowClear
            showSearch
            placeholder={t("pages.devices.form.selectGroup")}
            optionFilterProp="label"
            options={groupOptions}
          />
        </Form.Item>
        <Form.Item label={t("pages.devices.form.profile")} name="profile_id">
          <Select
            allowClear
            showSearch
            placeholder={t("pages.devices.form.selectProfile")}
            optionFilterProp="label"
            options={profiles.map((profile) => ({
              value: profile.id,
              label: profile.name,
            }))}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.ruleChain")}
          name="rule_chain_group_id"
          extra={t("pages.devices.form.ruleChainHint")}
        >
          <Select
            allowClear
            showSearch
            placeholder={t("pages.devices.form.ruleChainPlaceholder")}
            optionFilterProp="label"
            options={(groupsQuery.data ?? []).map((group) => ({
              value: group.id,
              label: group.is_default
                ? `${group.name} (${t("pages.ruleChains.defaultTag")})`
                : group.name,
            }))}
          />
        </Form.Item>
        <Form.Item
          label={t("pages.devices.form.notes")}
          name="notes"
          rules={[{ max: 4096, message: t("pages.devices.form.maxLengthNotes") }]}
        >
          <Input.TextArea
            rows={3}
            placeholder={t("pages.devices.form.notesPlaceholder")}
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
