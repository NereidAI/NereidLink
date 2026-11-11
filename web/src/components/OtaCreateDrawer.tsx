// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OtaCreateDrawer starts one firmware rollout. The operator chooses a
// platform-hosted package (selected from the firmware repository) or supplies
// a legacy external HTTPS URL descriptor, and picks targets either from an
// explicit device list or a dynamic profile/group/customer selector resolved
// once at creation (ADR-0006d §1). An optional advanced section configures
// canary batching and the abort/watchdog policy; empty fields inherit the
// server's safe defaults.

import { useMemo } from "react";
import {
  App as AntApp,
  Button,
  Collapse,
  Divider,
  Drawer,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  createOtaTask,
  type OtaRolloutPolicy,
  type OtaTargetSelector,
} from "../api/ota";
import { listFirmware, type FirmwarePackage } from "../api/firmware";
import { listDevices, listProfiles, type Device } from "../api/devices";
import { listCustomers, listDeviceGroups } from "../api/ownership";
import { extractError } from "../api/client";

interface OtaCreateDrawerProps {
  open: boolean;
  onClose: () => void;
  // presetDevices optionally preselects targets (e.g. launched from a device
  // selection on another page).
  presetDevices?: string[];
  // lockTargets hides the explicit/selector target switch and forces the
  // rollout to presetDevices, used by a single-device launch point.
  lockTargets?: boolean;
}

interface FormValues {
  source: "hosted" | "external";
  firmware_id?: string;
  targetMode: "explicit" | "selector";
  deviceIds?: string[];
  selProfile?: string;
  selGroup?: string;
  selCustomer?: string;
  success_policy?: "device_reported" | "confirmed_after_reboot";
  extVersion?: string;
  extUrl?: string;
  extSha?: string;
  extSize?: number;
  batchMode?: "none" | "percent" | "size";
  batchPercent?: number;
  batchSize?: number;
  batchPauseSeconds?: number;
  failureAbortPercent?: number;
  maxConsecutiveFailures?: number;
  stallTimeoutSeconds?: number;
  confirmTimeoutSeconds?: number;
}

// OtaCreateDrawer renders the rollout creation form.
export default function OtaCreateDrawer({
  open,
  onClose,
  presetDevices,
  lockTargets = false,
}: OtaCreateDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const source = Form.useWatch("source", form);
  const selectedFirmware = Form.useWatch("firmware_id", form);
  const targetMode = Form.useWatch("targetMode", form) ?? "explicit";
  const batchMode = Form.useWatch("batchMode", form) ?? "none";
  const selProfile = Form.useWatch("selProfile", form);
  const selGroup = Form.useWatch("selGroup", form);
  const selCustomer = Form.useWatch("selCustomer", form);

  const firmwareQuery = useQuery({
    queryKey: ["firmware", "all-for-rollout"],
    queryFn: () =>
      listFirmware({ page: 1, page_size: 500 }).then((page) => page.items),
    enabled: open,
  });

  const devicesQuery = useQuery({
    queryKey: ["devices", "all-for-rollout"],
    queryFn: () =>
      listDevices({ state: "ACTIVE", page: 1, page_size: 1000 }).then(
        (page) => page.items,
      ),
    enabled: open,
  });
  const profilesQuery = useQuery({
    queryKey: ["device-profiles", "for-rollout"],
    queryFn: listProfiles,
    enabled: open,
  });
  const customersQuery = useQuery({
    queryKey: ["customers", "for-rollout"],
    queryFn: listCustomers,
    enabled: open,
  });
  const groupsQuery = useQuery({
    queryKey: ["device-groups", "for-rollout"],
    queryFn: listDeviceGroups,
    enabled: open,
  });

  // chosenPackage resolves the selected repository package to drive the
  // model-compatible device hinting.
  const chosenPackage = useMemo(
    () =>
      firmwareQuery.data?.find((pkg) => pkg.id === selectedFirmware) as
        | FirmwarePackage
        | undefined,
    [firmwareQuery.data, selectedFirmware],
  );

  // Model-bound packages restrict both the explicit picker and the profile
  // dimension of the selector to that one model.
  const boundProfileId = chosenPackage?.profile_id ?? "";

  // candidateDevices narrows the explicit picker by package model and by any
  // selector-independent fleet filter (explicit mode ignores selector dims).
  const deviceOptions = useMemo(() => {
    const devices = devicesQuery.data ?? [];
    const eligible = boundProfileId
      ? devices.filter((device) => device.profile_id === boundProfileId)
      : devices;
    return eligible.map((device: Device) => ({
      value: device.id,
      label: device.canonical_name,
    }));
  }, [devicesQuery.data, boundProfileId]);

  // selectorPreview counts devices matching the current selector dimensions
  // (client-side, capped by the fetched page) so the operator sees the blast
  // radius before creating the task.
  const selectorPreview = useMemo(() => {
    if (targetMode !== "selector") return 0;
    return (devicesQuery.data ?? []).filter(
      (d) =>
        (!boundProfileId || d.profile_id === boundProfileId) &&
        (!selProfile || d.profile_id === selProfile) &&
        (!selGroup || d.group_id === selGroup) &&
        (!selCustomer || d.customer_id === selCustomer),
    ).length;
  }, [
    targetMode,
    devicesQuery.data,
    boundProfileId,
    selProfile,
    selGroup,
    selCustomer,
  ]);

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      // Build the target selection: either a snapshot UUID list or a
      // selector; the two are mutually exclusive server-side.
      let devices: string[] | undefined;
      let selector: OtaTargetSelector | undefined;
      if (values.targetMode === "selector") {
        selector = {
          profile_id: boundProfileId || values.selProfile || undefined,
          group_id: values.selGroup || undefined,
          customer_id: values.selCustomer || undefined,
        };
        if (
          !selector.profile_id &&
          !selector.group_id &&
          !selector.customer_id
        ) {
          throw new Error(t("pages.ota.modal.selectorRequired"));
        }
      } else {
        devices = values.deviceIds ?? [];
        if (devices.length === 0) {
          throw new Error(t("pages.ota.modal.devicesRequired"));
        }
      }

      // Advanced rollout policy; only fields the operator filled are sent,
      // so unset ones inherit the server defaults.
      const policy: OtaRolloutPolicy = {};
      if (values.batchMode === "percent") {
        policy.batch_percent = values.batchPercent;
      } else if (values.batchMode === "size") {
        policy.batch_size = values.batchSize;
      }
      if (values.batchPauseSeconds !== undefined && values.batchPauseSeconds > 0) {
        policy.batch_pause_seconds = values.batchPauseSeconds;
      }
      if (values.failureAbortPercent !== undefined) {
        policy.failure_abort_percent = values.failureAbortPercent;
      }
      if (values.maxConsecutiveFailures !== undefined) {
        policy.max_consecutive_failures = values.maxConsecutiveFailures;
      }
      if (values.stallTimeoutSeconds !== undefined) {
        policy.stall_timeout_seconds = values.stallTimeoutSeconds;
      }
      if (values.confirmTimeoutSeconds !== undefined) {
        policy.confirm_timeout_seconds = values.confirmTimeoutSeconds;
      }

      const common = {
        devices,
        selector,
        success_policy: values.success_policy ?? "device_reported",
        policy,
      };
      if (values.source === "hosted") {
        return createOtaTask({
          ...common,
          firmware_id: values.firmware_id,
        });
      }
      return createOtaTask({
        ...common,
        package: {
          version: values.extVersion?.trim() ?? "",
          url: values.extUrl?.trim() ?? "",
          sha256: values.extSha?.trim() ?? "",
          size: values.extSize ?? 0,
        },
      });
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["ota-tasks"] });
      message.success(t("pages.ota.toasts.created"));
      onClose();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      title={t("pages.ota.modal.title")}
      onClose={onClose}
      footer={null}
      destroyOnHidden
      width={720}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{
          source: "hosted",
          targetMode:
            lockTargets || (presetDevices && presetDevices.length > 0)
              ? "explicit"
              : "selector",
          deviceIds: presetDevices ?? [],
          success_policy: "device_reported",
          batchMode: "none",
        }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item name="source" label={t("pages.ota.modal.source")}>
          <Radio.Group>
            <Radio value="hosted">{t("pages.ota.modal.hosted")}</Radio>
            <Radio value="external">{t("pages.ota.modal.external")}</Radio>
          </Radio.Group>
        </Form.Item>

        <Form.Item
          name="success_policy"
          label={t("pages.ota.modal.successPolicy")}
          tooltip={t("pages.ota.modal.successPolicyHint")}
        >
          <Radio.Group>
            <Radio value="device_reported">
              {t("pages.ota.modal.policyDeviceReported")}
            </Radio>
            <Radio value="confirmed_after_reboot">
              {t("pages.ota.modal.policyConfirmedAfterReboot")}
            </Radio>
          </Radio.Group>
        </Form.Item>

        {source !== "external" ? (
          <Form.Item
            name="firmware_id"
            label={t("pages.ota.modal.package")}
            rules={[
              { required: true, message: t("pages.ota.modal.packageRequired") },
            ]}
          >
            <Select
              showSearch
              optionFilterProp="label"
              placeholder={t("pages.ota.modal.package")}
              loading={firmwareQuery.isLoading}
              options={(firmwareQuery.data ?? []).map((pkg) => ({
                value: pkg.id,
                label: `${pkg.name} · ${pkg.version}${
                  pkg.profile_id
                    ? ""
                    : ` · ${t("pages.firmware.profileGeneric")}`
                }`,
              }))}
            />
          </Form.Item>
        ) : (
          <>
            <Form.Item
              name="extVersion"
              label={t("pages.ota.modal.extVersion")}
              rules={[{ required: true }]}
            >
              <Input />
            </Form.Item>
            <Form.Item
              name="extUrl"
              label={t("pages.ota.modal.extUrl")}
              rules={[{ required: true, type: "url" }]}
            >
              <Input placeholder="https://" />
            </Form.Item>
            <Form.Item
              name="extSha"
              label={t("pages.ota.modal.extSha")}
              rules={[{ required: true, len: 64 }]}
            >
              <Input />
            </Form.Item>
            <Form.Item
              name="extSize"
              label={t("pages.ota.modal.extSize")}
              rules={[{ required: true }]}
            >
              <InputNumber min={0} style={{ width: "100%" }} />
            </Form.Item>
          </>
        )}

        <Divider style={{ margin: "8px 0 16px" }} />

        {!lockTargets && (
          <Form.Item name="targetMode" label={t("pages.ota.modal.targetMode")}>
            <Radio.Group>
              <Radio value="explicit">{t("pages.ota.modal.targetExplicit")}</Radio>
              <Radio value="selector">{t("pages.ota.modal.targetSelector")}</Radio>
            </Radio.Group>
          </Form.Item>
        )}

        {targetMode === "explicit" ? (
          <Form.Item
            name="deviceIds"
            label={t("pages.ota.modal.devices")}
            rules={[
              {
                validator: (_, value: string[]) =>
                  value && value.length > 0
                    ? Promise.resolve()
                    : Promise.reject(
                        new Error(t("pages.ota.modal.devicesRequired")),
                      ),
              },
            ]}
          >
            <Select
              mode="multiple"
              showSearch
              optionFilterProp="label"
              allowClear={!lockTargets}
              disabled={lockTargets}
              placeholder={t("pages.ota.modal.devices")}
              loading={devicesQuery.isLoading}
              options={deviceOptions}
            />
          </Form.Item>
        ) : (
          <>
            <Form.Item name="selProfile" label={t("pages.ota.modal.selProfile")}>
              <Select
                allowClear
                showSearch
                optionFilterProp="label"
                disabled={!!boundProfileId}
                placeholder={t("pages.ota.modal.anyProfile")}
                loading={profilesQuery.isLoading}
                options={(profilesQuery.data ?? []).map((p) => ({
                  value: p.id,
                  label: p.name,
                }))}
              />
            </Form.Item>
            <Form.Item name="selGroup" label={t("pages.ota.modal.selGroup")}>
              <Select
                allowClear
                showSearch
                optionFilterProp="label"
                placeholder={t("pages.ota.modal.anyGroup")}
                loading={groupsQuery.isLoading}
                options={(groupsQuery.data ?? []).map((g) => ({
                  value: g.id,
                  label: g.name,
                }))}
              />
            </Form.Item>
            <Form.Item name="selCustomer" label={t("pages.ota.modal.selCustomer")}>
              <Select
                allowClear
                showSearch
                optionFilterProp="label"
                placeholder={t("pages.ota.modal.anyCustomer")}
                loading={customersQuery.isLoading}
                options={(customersQuery.data ?? []).map((c) => ({
                  value: c.id,
                  label: c.name,
                }))}
              />
            </Form.Item>
            <Form.Item
              shouldUpdate={(prev, next) =>
                prev.selProfile !== next.selProfile ||
                prev.selGroup !== next.selGroup ||
                prev.selCustomer !== next.selCustomer ||
                prev.firmware_id !== next.firmware_id
              }
              noStyle
            >
              <p style={{ marginTop: -8, color: "rgba(0,0,0,0.45)" }}>
                {t("pages.ota.modal.selectorPreview", { count: selectorPreview })}
              </p>
            </Form.Item>
          </>
        )}

        <Collapse
          ghost
          items={[
            {
              key: "advanced",
              label: t("pages.ota.modal.advanced"),
              children: (
                <>
                  <Form.Item name="batchMode" label={t("pages.ota.modal.batchMode")}>
                    <Radio.Group>
                      <Radio value="none">
                        {t("pages.ota.modal.batchNone")}
                      </Radio>
                      <Radio value="percent">
                        {t("pages.ota.modal.batchPercent")}
                      </Radio>
                      <Radio value="size">{t("pages.ota.modal.batchSize")}</Radio>
                    </Radio.Group>
                  </Form.Item>
                  {batchMode === "percent" && (
                    <Form.Item
                      name="batchPercent"
                      label={t("pages.ota.modal.batchPercentLabel")}
                      rules={[
                        { required: true },
                        {
                          validator: (_, v?: number) =>
                            v !== undefined && v >= 1 && v <= 100
                              ? Promise.resolve()
                              : Promise.reject(
                                  new Error(t("pages.ota.modal.batchPercentHint")),
                                ),
                        },
                      ]}
                      extra={t("pages.ota.modal.batchPercentHint")}
                    >
                      <InputNumber min={1} max={100} addonAfter="%" />
                    </Form.Item>
                  )}
                  {batchMode === "size" && (
                    <Form.Item
                      name="batchSize"
                      label={t("pages.ota.modal.batchSizeLabel")}
                      rules={[{ required: true }]}
                      extra={t("pages.ota.modal.batchSizeHint")}
                    >
                      <InputNumber min={1} addonAfter={t("pages.ota.modal.devicesUnit")} />
                    </Form.Item>
                  )}
                  <Form.Item
                    name="batchPauseSeconds"
                    label={t("pages.ota.modal.batchPause")}
                    extra={t("pages.ota.modal.batchPauseHint")}
                  >
                    <InputNumber min={0} addonAfter="s" />
                  </Form.Item>
                  <Form.Item
                    name="failureAbortPercent"
                    label={t("pages.ota.modal.failureAbortPercent")}
                    extra={t("pages.ota.modal.failureAbortPercentHint")}
                  >
                    <InputNumber min={1} max={100} addonAfter="%" />
                  </Form.Item>
                  <Form.Item
                    name="maxConsecutiveFailures"
                    label={t("pages.ota.modal.maxConsecutiveFailures")}
                    extra={t("pages.ota.modal.maxConsecutiveFailuresHint")}
                  >
                    <InputNumber min={1} />
                  </Form.Item>
                  <Form.Item
                    name="stallTimeoutSeconds"
                    label={t("pages.ota.modal.stallTimeout")}
                    extra={t("pages.ota.modal.stallTimeoutHint")}
                  >
                    <InputNumber min={60} addonAfter="s" />
                  </Form.Item>
                  <Form.Item
                    name="confirmTimeoutSeconds"
                    label={t("pages.ota.modal.confirmTimeout")}
                    extra={t("pages.ota.modal.confirmTimeoutHint")}
                    style={{ marginBottom: 0 }}
                  >
                    <InputNumber min={60} addonAfter="s" />
                  </Form.Item>
                </>
              ),
            },
          ]}
        />

        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Button onClick={onClose} style={{ marginRight: 8 }}>
            {t("common.cancel")}
          </Button>
          <Button type="primary" htmlType="submit" loading={mutation.isPending}>
            {t("pages.ota.create")}
          </Button>
        </Form.Item>
      </Form>
    </Drawer>
  );
}
