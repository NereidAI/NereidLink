// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// EscalationChainFormDrawer is the create/edit drawer for alarm escalation
// chains. A Form.List renders the ordered steps; each step carries a
// delay from alarm start, a target role and ack/clear stop flags.

import { App as AntApp, Button, Divider, Drawer, Form, Input, InputNumber, Select, Space, Switch } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  createEscalationChain,
  updateEscalationChain,
  type EscalationChain,
} from "../api/escalations";
import { listDeviceGroups } from "../api/ownership";
import { extractError } from "../api/client";

interface EscalationChainFormDrawerProps {
  // open controls visibility.
  open: boolean;

  // chain is present when editing.
  chain?: EscalationChain;

  // onClose hides the drawer without saving.
  onClose: () => void;
}

// StepFormValues is one row of the steps Form.List.
interface StepFormValues {
  delay_seconds: number;
  target_role: string;
  target_group_id?: string;
  stop_on_ack: boolean;
  stop_on_clear: boolean;
}

// FormValues is the chain form shape.
interface FormValues {
  name: string;
  enabled: boolean;
  repeat_interval_seconds: number;
  repeat_target_role: string;
  repeat_target_group_id?: string;
  steps: StepFormValues[];
}

// targetRoles lists the selectable recipient roles.
const targetRoles = ["TENANT_ADMIN", "ENGINEER", "CUSTOMER_USER", "READ_ONLY"];

// EscalationChainFormDrawer renders the chain editor.
export default function EscalationChainFormDrawer({
  open,
  chain,
  onClose,
}: EscalationChainFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const repeatInterval = Form.useWatch("repeat_interval_seconds", form);
  const remindersOn = (repeatInterval ?? 0) > 0;

  const { data: groups } = useQuery({
    queryKey: ["device-groups"],
    queryFn: listDeviceGroups,
  });
  const groupOptions = (groups ?? []).map((group) => ({
    value: group.id,
    label: group.parent_id ? `↳ ${group.name}` : group.name,
  }));

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      const interval = values.repeat_interval_seconds ?? 0;
      const input = {
        name: values.name,
        enabled: values.enabled,
        repeat_interval_seconds: interval,
        repeat_target_role: interval > 0 ? values.repeat_target_role : "",
        repeat_target_group_id:
          interval > 0 ? values.repeat_target_group_id ?? "" : "",
        steps: values.steps.map((step) => ({
          delay_seconds: step.delay_seconds,
          target_role: step.target_role,
          target_group_id: step.target_group_id ?? "",
          stop_on_ack: step.stop_on_ack,
          stop_on_clear: step.stop_on_clear,
        })),
      };
      return chain
        ? updateEscalationChain(chain.chain_id, input)
        : createEscalationChain(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["escalation-chains"] });
      message.success(
        chain
          ? t("pages.escalations.toasts.updated")
          : t("pages.escalations.toasts.created"),
      );
      onClose();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      title={chain ? t("pages.escalations.form.editTitle") : t("pages.escalations.form.createTitle")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      width={720}
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({
            name: chain?.name ?? "",
            enabled: chain?.enabled ?? true,
            repeat_interval_seconds: chain?.repeat_interval_seconds ?? 0,
            repeat_target_role: chain?.repeat_target_role ?? "TENANT_ADMIN",
            repeat_target_group_id: chain?.repeat_target_group_id ?? undefined,
            steps:
              chain?.steps.map((step) => ({
                delay_seconds: step.delay_seconds,
                target_role: step.target_role,
                target_group_id: step.target_group_id ?? undefined,
                stop_on_ack: step.stop_on_ack,
                stop_on_clear: step.stop_on_clear,
              })) ?? [
                { delay_seconds: 300, target_role: "ENGINEER", target_group_id: undefined, stop_on_ack: true, stop_on_clear: true },
              ],
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
        <Space wrap align="end">
          <Form.Item
            label={t("common.name")}
            name="name"
            rules={[{ required: true, message: t("pages.escalations.form.nameRequired") }]}
          >
            <Input placeholder={t("pages.escalations.form.namePlaceholder")} />
          </Form.Item>
          <Form.Item label={t("common.enabled")} name="enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
        </Space>

        <Divider style={{ margin: "8px 0" }}>{t("pages.escalations.form.repeatTitle")}</Divider>
        <Space align="baseline" wrap>
          <Form.Item
            label={t("pages.escalations.form.repeatInterval")}
            name="repeat_interval_seconds"
            tooltip={t("pages.escalations.form.repeatHint")}
          >
            <InputNumber min={0} step={60} style={{ width: 140 }} />
          </Form.Item>
          <Form.Item
            label={t("pages.escalations.form.repeatRole")}
            name="repeat_target_role"
            rules={remindersOn ? [{ required: true, message: t("pages.escalations.form.roleRequired") }] : []}
          >
            <Select
              style={{ width: 170 }}
              disabled={!remindersOn}
              options={targetRoles.map((role) => ({ value: role, label: role }))}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.escalations.form.targetGroup")}
            name="repeat_target_group_id"
            tooltip={t("pages.escalations.form.targetGroupHint")}
          >
            <Select
              style={{ width: 200 }}
              allowClear
              disabled={!remindersOn}
              placeholder={t("pages.escalations.form.targetGroupPlaceholder")}
              options={groupOptions}
            />
          </Form.Item>
        </Space>

        <Form.List
          name="steps"
          rules={[
            {
              validator: async (_, steps) => {
                if (!steps || steps.length === 0) {
                  return Promise.reject(new Error(t("pages.escalations.form.stepsRequired")));
                }
                return Promise.resolve();
              },
            },
          ]}
        >
          {(fields, { add, remove }) => (
            <Space direction="vertical" style={{ display: "flex" }}>
              {fields.map((field, index) => (
                <Space key={field.key} align="baseline" wrap>
                  <span style={{ minWidth: 48 }}>#{index + 1}</span>
                  <Form.Item
                    {...field}
                    label={t("pages.escalations.form.delay")}
                    name={[field.name, "delay_seconds"]}
                    rules={[{ required: true, message: t("pages.escalations.form.delayRequired") }]}
                  >
                    <InputNumber min={0} style={{ width: 110 }} />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    label={t("pages.escalations.form.targetRole")}
                    name={[field.name, "target_role"]}
                    rules={[{ required: true, message: t("pages.escalations.form.roleRequired") }]}
                  >
                    <Select
                      style={{ width: 160 }}
                      options={targetRoles.map((role) => ({ value: role, label: role }))}
                    />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    label={t("pages.escalations.form.targetGroup")}
                    name={[field.name, "target_group_id"]}
                    tooltip={t("pages.escalations.form.targetGroupHint")}
                  >
                    <Select
                      style={{ width: 180 }}
                      allowClear
                      placeholder={t("pages.escalations.form.targetGroupPlaceholder")}
                      options={groupOptions}
                    />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    label={t("pages.escalations.form.stopOnAck")}
                    name={[field.name, "stop_on_ack"]}
                    valuePropName="checked"
                  >
                    <Switch size="small" />
                  </Form.Item>
                  <Form.Item
                    {...field}
                    label={t("pages.escalations.form.stopOnClear")}
                    name={[field.name, "stop_on_clear"]}
                    valuePropName="checked"
                  >
                    <Switch size="small" />
                  </Form.Item>
                  <Button
                    type="text"
                    danger
                    icon={<DeleteOutlined />}
                    onClick={() => remove(field.name)}
                  />
                </Space>
              ))}
              <Button type="dashed" icon={<PlusOutlined />} onClick={() => add({ delay_seconds: 600, target_role: "TENANT_ADMIN", target_group_id: undefined, stop_on_ack: true, stop_on_clear: true })} block>
                {t("pages.escalations.form.addStep")}
              </Button>
            </Space>
          )}
        </Form.List>

        <Form.Item style={{ marginBottom: 0, marginTop: 16, textAlign: "right" }}>
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
