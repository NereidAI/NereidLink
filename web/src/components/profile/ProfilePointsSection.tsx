// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfilePointsSection edits the descriptor set (the point library) of one
// profile on the detail page. The read view is a compact table so an edge
// profile with dozens of points stays scannable; editing mounts a Form.List
// of DescriptorCard rows. Point addresses live in a parallel array outside
// the antd form store (rc-field-form cannot hold the per-row JSON objects),
// and the whole profile is PUT through profileToInput so saving points never
// touches the connector template. Vendor spec columns are editable only on
// console-authored (manual) profiles; address and the protocol family stay
// maintainable on every source (ADR-0032 §5, revised).

import { App as AntApp, Button, Form, Table, Tag, Typography } from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import type { Profile, ProfileInput, Descriptor } from "../../api/profiles";
import { updateProfile } from "../../api/profiles";
import { useCatalogDictionary } from "../connector/useProtocolCatalog";
import FilterSearchBar from "../FilterSearchBar";
import {
  descriptorFormValues,
  descriptorsToInput,
  profileToInput,
} from "../profilePayload";
import { extractError } from "../../api/client";
import { DescriptorCard } from "./descriptorFields";
import type { DescriptorFormValues } from "./descriptorModel";
import { EditableSection } from "./EditableSection";

interface PointsFormValues {
  descriptors: DescriptorFormValues[];
}

interface ProfilePointsSectionProps {
  // profile is the stored profile being edited.
  profile: Profile;
  // editing/onEdit/onCancel are the shared read/edit flag of this section.
  editing: boolean;
  onEdit: () => void;
  onCancel: () => void;
}

// ProfilePointsSection renders the point descriptor card.
export function ProfilePointsSection({
  profile,
  editing,
  onEdit,
  onCancel,
}: ProfilePointsSectionProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<PointsFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  // Addresses are form-external state, seeded fresh when the section enters
  // edit mode (the form itself is mounted fresh at that point via key).
  const [addresses, setAddresses] = useState<Record<string, unknown>[]>([]);

  // pointKeyword filters the read-only descriptor table client-side over
  // the key, type, unit and address JSON.
  const [pointKeyword, setPointKeyword] = useState("");

  const filteredDescriptors = useMemo(() => {
    const needle = pointKeyword.trim().toLowerCase();
    const all = profile.descriptors ?? [];
    if (!needle) return all;
    return all.filter((descriptor) =>
      [
        descriptor.measurement_key,
        descriptor.data_type,
        descriptor.read_write,
        descriptor.unit,
        descriptor.address ? JSON.stringify(descriptor.address) : "",
      ]
        .filter(Boolean)
        .some((field) => String(field).toLowerCase().includes(needle)),
    );
  }, [profile.descriptors, pointKeyword]);

  const specEditable = profile.source === "manual";
  const protocolDict = useCatalogDictionary(profile.protocol);

  const setAddressAt = (index: number, address: Record<string, unknown>) =>
    setAddresses((previous) => {
      const next = [...previous];
      next[index] = address;
      return next;
    });
  const removeAddressAt = (index: number) =>
    setAddresses((previous) => previous.filter((_, i) => i !== index));

  const mutation = useMutation({
    mutationFn: async (values: PointsFormValues) => {
      const input: ProfileInput = profileToInput(profile);
      input.descriptors = descriptorsToInput(
        values.descriptors ?? [],
        addresses,
        specEditable,
      );
      await updateProfile(profile.id, input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(t("pages.profiles.toasts.updated"));
      onCancel();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns: ColumnsType<Descriptor> = [
    {
      title: t("pages.profiles.form.keyPlaceholder"),
      dataIndex: "measurement_key",
      render: (value: string) => <Typography.Text strong>{value}</Typography.Text>,
    },
    {
      title: t("pages.profiles.form.dataType"),
      dataIndex: "data_type",
      width: 110,
      render: (value?: string) => value ?? <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: t("pages.profiles.form.readWrite"),
      dataIndex: "read_write",
      width: 90,
      render: (value?: string) => value ?? <Typography.Text type="secondary">—</Typography.Text>,
    },
    { title: t("pages.profiles.form.unit"), dataIndex: "unit", width: 90 },
    {
      title: t("pages.profiles.form.expectedPeriodShort"),
      dataIndex: "expected_period_seconds",
      width: 110,
      render: (value?: number | null) =>
        value ? (
          <Typography.Text>{t("pages.profiles.form.secondsValue", { value })}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
    },
    {
      title: t("pages.profiles.form.address"),
      key: "address",
      render: (_, record) =>
        record.address && Object.keys(record.address).length ? (
          <Typography.Text code style={{ fontSize: 12 }}>
            {JSON.stringify(record.address)}
          </Typography.Text>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
    },
    {
      title: "",
      dataIndex: "inactive",
      width: 90,
      render: (inactive?: boolean) =>
        inactive ? <Tag>{t("pages.profiles.form.inactive")}</Tag> : null,
    },
  ];

  const readView = (
    <>
      <FilterSearchBar
        searchValue={pointKeyword}
        onSearchChange={setPointKeyword}
        searchPlaceholder={t("pages.profiles.filters.pointSearchPlaceholder")}
      />
      <Table
        rowKey={(record) => record.id ?? record.measurement_key}
        size="small"
        columns={columns}
        dataSource={filteredDescriptors}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{
          emptyText: pointKeyword.trim()
            ? t("common.noFilterResults")
            : t("pages.profiles.detail.noPoints"),
        }}
      />
    </>
  );

  return (
    <EditableSection
      title={t("pages.profiles.detail.sections.points", {
        count: profile.descriptors?.length ?? 0,
      })}
      editing={editing}
      onEdit={() => {
        setAddresses((profile.descriptors ?? []).map((d) => d.address ?? {}));
        onEdit();
      }}
      onCancel={onCancel}
      readView={readView}
      form={form}
      saving={mutation.isPending}
    >
      <Form
        key={profile.id}
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{ descriptors: descriptorFormValues(profile) }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.List name="descriptors">
          {(fields, { add, remove }) => (
            <>
              {fields.map((field) => (
                <DescriptorCard
                  key={field.key}
                  fieldName={field.name}
                  form={form}
                  remove={(index) => {
                    remove(index);
                    removeAddressAt(index);
                  }}
                  specEditable={specEditable}
                  protocolDict={protocolDict}
                  address={addresses[field.name] ?? {}}
                  onAddressChange={(next) => setAddressAt(field.name, next)}
                />
              ))}
              <Button
                type="dashed"
                block
                icon={<PlusOutlined />}
                onClick={() => {
                  add({
                    transform_type: "identity",
                    scale: 1,
                    offset: 0,
                    precision: -1,
                    breakpoints: [],
                    enum_pairs: [],
                    data_type: undefined,
                  });
                  setAddresses((previous) => [
                    ...previous,
                    protocolDict ? protocolDict.defaultPointAddress() : {},
                  ]);
                }}
                style={{ marginBottom: 16 }}
              >
                {t("pages.profiles.form.addDescriptor")}
              </Button>
            </>
          )}
        </Form.List>
      </Form>
    </EditableSection>
  );
}
