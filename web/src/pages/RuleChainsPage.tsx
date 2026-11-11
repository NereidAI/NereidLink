// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// RuleChainsPage lists chain groups with their version summary and opens
// the visual editor. A chain group is the stable identity that device
// profiles and devices bind to; the latest draft of a group can be
// activated directly from the list.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { EditOutlined, DeleteOutlined, PlusOutlined, RocketOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import {
  activateChain,
  createGroup,
  deleteGroup,
  listChainVersions,
  listGroups,
  setGroupEnabled,
  type GroupMeta,
} from "../api/ruleChains";
import { extractError } from "../api/client";

// RuleChainsPage renders the chain group list.
export default function RuleChainsPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [createOpen, setCreateOpen] = useState(false);
  const [createForm] = Form.useForm<{ name: string }>();
  const [keyword, setKeyword] = useState("");
  const [defaultFilter, setDefaultFilter] = useState("");

  const query = useQuery({ queryKey: ["rule-chain-groups"], queryFn: listGroups });
  const groups = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return (query.data ?? []).filter((group) => {
      if (defaultFilter === "default" && !group.is_default) return false;
      if (defaultFilter === "custom" && group.is_default) return false;
      return !needle || group.name.toLowerCase().includes(needle);
    });
  }, [query.data, keyword, defaultFilter]);

  // activateLatest activates the newest draft version of a group.
  const activateMutation = useMutation({
    mutationFn: async (group: GroupMeta) => {
      const versions = await listChainVersions(group.id);
      const draft = versions.find((version) => version.state === "DRAFT");
      if (!draft) {
        throw new Error(t("pages.ruleChains.noDraft"));
      }
      await activateChain(draft.id);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["rule-chain-groups"] });
      message.success(t("pages.ruleChains.activated"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // createMutation creates an empty group then opens its editor.
  const createMutation = useMutation({
    mutationFn: (name: string) => createGroup(name),
    onSuccess: (groupID) => {
      message.success(t("pages.ruleChains.groupCreated"));
      setCreateOpen(false);
      createForm.resetFields();
      navigate(`/rule-chains/g/${groupID}`);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // enableMutation suspends or resumes one chain group.
  const enableMutation = useMutation({
    mutationFn: (variables: { group: GroupMeta; enabled: boolean }) =>
      setGroupEnabled(variables.group.id, variables.enabled),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["rule-chain-groups"] });
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // deleteMutation removes one unreferenced, non-default chain group.
  const deleteMutation = useMutation({
    mutationFn: (group: GroupMeta) => deleteGroup(group.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["rule-chain-groups"] });
      message.success(t("pages.ruleChains.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("common.name"),
      dataIndex: "name",
      render: (name: string, record: GroupMeta) => (
        <Space>
          <Typography.Text strong>{name}</Typography.Text>
          {record.is_default && (
            <Tag color="blue">{t("pages.ruleChains.defaultTag")}</Tag>
          )}
          {!record.enabled && (
            <Tag>{t("pages.ruleChains.disabledTag")}</Tag>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.ruleChains.activeVersion"),
      dataIndex: "active_version",
      width: 130,
      render: (version: number) =>
        version > 0 ? <Tag color="success">v{version}</Tag> : <Tag>{t("pages.ruleChains.none")}</Tag>,
    },
    {
      title: t("pages.ruleChains.latestVersion"),
      dataIndex: "latest_version",
      width: 120,
      render: (version: number) => `v${version}`,
    },
    {
      title: t("common.enabled"),
      dataIndex: "enabled",
      width: 100,
      render: (enabled: boolean, record: GroupMeta) => (
        <Switch
          size="small"
          checked={enabled}
          loading={enableMutation.isPending}
          onChange={(checked) =>
            enableMutation.mutate({ group: record, enabled: checked })
          }
        />
      ),
    },
    {
      title: t("common.created"),
      dataIndex: "created_at",
      width: 220,
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 180,
      render: (_: unknown, record: GroupMeta) => (
        <Space>
          <Tooltip title={t("pages.ruleChains.openEditor")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => navigate(`/rule-chains/g/${record.id}`)}
            />
          </Tooltip>
          {record.latest_version > record.active_version && (
            <Popconfirm
              title={t("pages.ruleChains.confirm.title")}
              description={t("pages.ruleChains.confirm.desc")}
              onConfirm={() => activateMutation.mutate(record)}
            >
              <Tooltip title={t("pages.ruleChains.activate")}>
                <Button type="text" size="small" icon={<RocketOutlined />} />
              </Tooltip>
            </Popconfirm>
          )}
          {record.is_default ? (
            <Tooltip title={t("pages.ruleChains.deleteBlockedDefault")}>
              <span>
                <Button
                  type="text"
                  size="small"
                  danger
                  disabled
                  icon={<DeleteOutlined />}
                />
              </span>
            </Tooltip>
          ) : record.device_count + record.profile_count > 0 ? (
            <Tooltip
              title={t("pages.ruleChains.deleteBlockedBound", {
                devices: record.device_count,
                profiles: record.profile_count,
              })}
            >
              <span>
                <Button
                  type="text"
                  size="small"
                  danger
                  disabled
                  icon={<DeleteOutlined />}
                />
              </span>
            </Tooltip>
          ) : (
            <Popconfirm
              title={t("pages.ruleChains.confirmDeleteTitle", {
                name: record.name,
              })}
              description={t("pages.ruleChains.confirmDeleteDesc")}
              okText={t("common.delete")}
              okButtonProps={{ danger: true }}
              onConfirm={() => deleteMutation.mutate(record)}
            >
              <Tooltip title={t("common.delete")}>
                <Button type="text" size="small" danger icon={<DeleteOutlined />} />
              </Tooltip>
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.ruleChains.title")}
        subtitle={t("pages.ruleChains.subtitle")}
        helpId="rule-chains"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setCreateOpen(true)}
          >
            {t("pages.ruleChains.newChain")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("common.searchPlaceholder")}
          filters={[
            {
              key: "default",
              placeholder: t("pages.ruleChains.filterKind"),
              value: defaultFilter,
              onChange: setDefaultFilter,
              options: [
                { value: "default", label: t("pages.ruleChains.defaultTag") },
                { value: "custom", label: t("pages.ruleChains.customKind") },
              ],
            },
          ]}
        />
        <Table
          rowKey="id"
          loading={query.isFetching}
          columns={columns}
          dataSource={groups}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <Drawer
        open={createOpen}
        title={t("pages.ruleChains.createTitle")}
        width={720}
        onClose={() => setCreateOpen(false)}
        footer={
          <Space style={{ marginLeft: "auto" }}>
            <Button onClick={() => setCreateOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              loading={createMutation.isPending}
              onClick={() =>
                createForm.validateFields().then((values) =>
                  createMutation.mutate(values.name),
                )
              }
            >
              {t("common.create")}
            </Button>
          </Space>
        }
      >
        <Form form={createForm} layout="vertical" initialValues={{ name: "" }}>
          <Form.Item
            name="name"
            label={t("pages.ruleChains.nameLabel")}
            rules={[{ required: true, message: t("pages.ruleChains.nameRequired") }]}
          >
            <Input placeholder={t("pages.ruleChains.namePlaceholder")} />
          </Form.Item>
        </Form>
      </Drawer>
    </>
  );
}
