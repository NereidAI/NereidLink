// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileBasicSection edits the identity fields of one profile: its name, the
// ADR-0032 protocol family (open vocabulary; a known family unlocks
// structured fields elsewhere) and the bound rule-chain group. It is the
// first read-first card of the profile detail page. Saving PUTs the whole
// profile (the update endpoint replaces it) via profileToInput, overriding
// only these three fields so descriptors and the connector template survive.

import { App as AntApp, AutoComplete, Descriptions, Form, Input, Select } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { Profile, ProfileInput } from "../../api/profiles";
import { updateProfile } from "../../api/profiles";
import { listGroups } from "../../api/ruleChains";
import {
  familyOptions,
  useProtocolFamilies,
} from "../connector/useProtocolCatalog";
import { profileToInput } from "../profilePayload";
import { extractError } from "../../api/client";
import { EditableSection } from "./EditableSection";

interface BasicFormValues {
  name: string;
  protocol?: string;
  rule_chain_group_id?: string;
}

interface ProfileBasicSectionProps {
  // profile is the stored profile being edited.
  profile: Profile;
  // editing/onEdit/onCancel are the shared read/edit flag of this section.
  editing: boolean;
  onEdit: () => void;
  onCancel: () => void;
}

// ProfileBasicSection renders the identity card.
export function ProfileBasicSection({
  profile,
  editing,
  onEdit,
  onCancel,
}: ProfileBasicSectionProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<BasicFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const groupsQuery = useQuery({ queryKey: ["rule-chain-groups"], queryFn: listGroups });

  const familiesQuery = useProtocolFamilies();
  const protocolOptions = useMemo(
    () => familyOptions(familiesQuery.data),
    [familiesQuery.data],
  );

  const groupName = useMemo(() => {
    const group = (groupsQuery.data ?? []).find((g) => g.id === profile.rule_chain_group_id);
    if (!group) return "";
    return group.is_default
      ? `${group.name} (${t("pages.ruleChains.defaultTag")})`
      : group.name;
  }, [groupsQuery.data, profile.rule_chain_group_id, t]);

  const mutation = useMutation({
    mutationFn: async (values: BasicFormValues) => {
      const input: ProfileInput = profileToInput(profile);
      input.name = values.name;
      input.protocol = values.protocol?.trim() || undefined;
      input.rule_chain_group_id = values.rule_chain_group_id;
      await updateProfile(profile.id, input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(t("pages.profiles.toasts.updated"));
      onCancel();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const readView = (
    <Descriptions column={1} size="small">
      <Descriptions.Item label={t("pages.profiles.form.profileName")}>
        {profile.name}
      </Descriptions.Item>
      <Descriptions.Item label={t("pages.profiles.form.protocol")}>
        {profile.protocol || "—"}
      </Descriptions.Item>
      <Descriptions.Item label={t("pages.profiles.form.ruleChain")}>
        {groupName || t("pages.profiles.form.ruleChainPlaceholder")}
      </Descriptions.Item>
    </Descriptions>
  );

  return (
    <EditableSection
      title={t("pages.profiles.detail.sections.basic")}
      editing={editing}
      onEdit={onEdit}
      onCancel={onCancel}
      readView={readView}
      form={form}
      saving={mutation.isPending}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{
          name: profile.name,
          protocol: profile.protocol ?? "",
          rule_chain_group_id: profile.rule_chain_group_id,
        }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.profiles.form.profileName")}
          name="name"
          rules={[{ required: true, message: t("pages.profiles.form.nameRequired") }]}
        >
          <Input placeholder={t("pages.profiles.form.namePlaceholder")} />
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
        <Form.Item
          label={t("pages.profiles.form.ruleChain")}
          name="rule_chain_group_id"
          extra={t("pages.profiles.form.ruleChainHint")}
        >
          <Select
            allowClear
            placeholder={t("pages.profiles.form.ruleChainPlaceholder")}
            options={(groupsQuery.data ?? []).map((group) => ({
              value: group.id,
              label: group.is_default
                ? `${group.name} (${t("pages.ruleChains.defaultTag")})`
                : group.name,
            }))}
          />
        </Form.Item>
      </Form>
    </EditableSection>
  );
}
