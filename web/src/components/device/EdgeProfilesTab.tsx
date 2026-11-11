// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// EdgeProfilesTab lists the EdgeX device profiles a gateway reported on
// v1/gateway/profiles, lets the operator search and inspect the cached raw
// resources, and adopts one into a native device profile on demand. Adoption
// is never automatic: the default action creates a same-name profile; a
// manual same-name profile returns 409 and opens a drawer offering either an
// descriptor overwrite (mode=update, preserving the connector template) or
// adoption under a different name (target_name).

import { useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Input,
  Drawer,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { EyeOutlined, ReloadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { extractError } from "../../api/client";
import {
  adoptEdgeProfile,
  getEdgeProfile,
  listEdgeProfiles,
  type AdoptEdgeProfileResult,
  type EdgeProfile,
} from "../../api/edgeProfiles";
import { formatDateTime } from "../../utils/format";

interface EdgeProfilesTabProps {
  // gatewayId is the reporting gateway device being detailed.
  gatewayId: string;
}

// conflictState carries the open conflict-resolution drawer context.
interface ConflictState {
  name: string;
  targetName: string;
}

// EdgeProfilesTab renders the cached profiles with search, detail and adopt.
export default function EdgeProfilesTab({ gatewayId }: EdgeProfilesTabProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();

  const profilesQuery = useQuery({
    queryKey: ["edge-profiles", gatewayId],
    queryFn: () => listEdgeProfiles(gatewayId),
  });
  // Memoized so downstream useMemo dependencies keep a stable identity
  // instead of seeing a fresh [] fallback on every render.
  const profiles = useMemo(() => profilesQuery.data ?? [], [profilesQuery.data]);

  const [keyword, setKeyword] = useState("");
  // detailName names the profile whose content drawer is open (or null).
  const [detailName, setDetailName] = useState<string | null>(null);
  // Last adopt result drives the skipped-resources summary drawer.
  const [result, setResult] = useState<AdoptEdgeProfileResult | null>(null);
  const [conflict, setConflict] = useState<ConflictState | null>(null);

  const filtered = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    if (!term) return profiles;
    return profiles.filter((p) => p.name.toLowerCase().includes(term));
  }, [profiles, keyword]);

  const detailQuery = useQuery({
    queryKey: ["edge-profile", gatewayId, detailName],
    queryFn: () => getEdgeProfile(gatewayId, detailName as string),
    enabled: !!detailName,
  });

  const adoptMutation = useMutation({
    mutationFn: (vars: { name: string; mode?: "update"; targetName?: string }) =>
      adoptEdgeProfile(gatewayId, vars.name, {
        mode: vars.mode,
        target_name: vars.targetName,
      }),
    onSuccess: (data) => {
      setResult(data);
      setConflict(null);
      queryClient.invalidateQueries({ queryKey: ["edge-profiles", gatewayId] });
      queryClient.invalidateQueries({ queryKey: ["profiles"] });
    },
    onError: (error, vars) => {
      const apiError = extractError(error);
      if (apiError.status === 409 && !vars.mode && !vars.targetName) {
        setConflict({ name: vars.name, targetName: vars.name });
        return;
      }
      message.error(apiError.message);
    },
  });

  const columns = [
    {
      title: t("pages.deviceDetail.edgeProfiles.columns.name"),
      dataIndex: "name",
      render: (name: string) => <Typography.Text strong>{name}</Typography.Text>,
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.columns.resources"),
      dataIndex: "resource_count",
      width: 100,
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.columns.lastSeen"),
      dataIndex: "last_seen_at",
      width: 190,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.columns.adopted"),
      dataIndex: "adopted_profile_id",
      width: 200,
      render: (id: string | undefined, record: EdgeProfile) =>
        id ? (
          <Link to={`/profiles/${id}`}>
            <Tag color="green">{record.name}</Tag>
          </Link>
        ) : (
          <Tag>{t("pages.deviceDetail.edgeProfiles.notAdopted")}</Tag>
        ),
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.columns.actions"),
      width: 180,
      render: (_: unknown, record: EdgeProfile) => (
        <Space size={4}>
          <Button
            size="small"
            icon={<EyeOutlined />}
            onClick={() => setDetailName(record.name)}
          >
            {t("pages.deviceDetail.edgeProfiles.view")}
          </Button>
          <Button
            size="small"
            type={record.adopted_profile_id ? "default" : "primary"}
            loading={adoptMutation.isPending}
            onClick={() => adoptMutation.mutate({ name: record.name })}
          >
            {record.adopted_profile_id
              ? t("pages.deviceDetail.edgeProfiles.readopt")
              : t("pages.deviceDetail.edgeProfiles.adopt")}
          </Button>
        </Space>
      ),
    },
  ];

  const resourceColumns = [
    {
      title: t("pages.deviceDetail.edgeProfiles.detail.name"),
      dataIndex: "name",
      width: 200,
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.detail.valueType"),
      dataIndex: "value_type",
      width: 130,
      render: (v: string) => <Tag>{v || "-"}</Tag>,
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.detail.units"),
      dataIndex: "units",
      width: 90,
      render: (v: string) => v || "-",
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.detail.description"),
      dataIndex: "description",
      render: (v: string) => v || "-",
    },
    {
      title: t("pages.deviceDetail.edgeProfiles.detail.status"),
      dataIndex: "translatable",
      width: 160,
      render: (translatable: boolean, row: { skip_reason?: string }) =>
        translatable ? (
          <Tag color="green">{t("pages.deviceDetail.edgeProfiles.detail.translatable")}</Tag>
        ) : (
          <Tooltip title={row.skip_reason}>
            <Tag color="red">{t("pages.deviceDetail.edgeProfiles.detail.skipped")}</Tag>
          </Tooltip>
        ),
    },
  ];

  return (
    <>
      <Space style={{ marginBottom: 12, width: "100%", justifyContent: "space-between" }}>
        <Input.Search
          allowClear
          style={{ maxWidth: 320 }}
          placeholder={t("pages.deviceDetail.edgeProfiles.searchPlaceholder")}
          value={keyword}
          onChange={(event) => setKeyword(event.target.value)}
        />
        <Space>
          <Typography.Text type="secondary">
            {t("pages.deviceDetail.edgeProfiles.hint")}
          </Typography.Text>
          <Tooltip title={t("common.refresh")}>
            <Button
              icon={<ReloadOutlined />}
              onClick={() => profilesQuery.refetch()}
              loading={profilesQuery.isFetching}
            />
          </Tooltip>
        </Space>
      </Space>
      <Table
        rowKey="name"
        loading={profilesQuery.isLoading}
        columns={columns}
        dataSource={filtered}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("pages.deviceDetail.edgeProfiles.empty") }}
      />

      <Drawer
        open={!!detailName}
        title={t("pages.deviceDetail.edgeProfiles.detailTitle", { name: detailName })}
        width={860}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setDetailName(null)}>
              {t("common.close")}
            </Button>
            <Button
              type="primary"
              loading={adoptMutation.isPending}
              onClick={() => detailName && adoptMutation.mutate({ name: detailName })}
            >
              {t("pages.deviceDetail.edgeProfiles.adopt")}
            </Button>
          </Space>
        }
        onClose={() => setDetailName(null)}
      >
        <Table
          size="small"
          rowKey="name"
          loading={detailQuery.isLoading}
          columns={resourceColumns}
          dataSource={detailQuery.data?.resources ?? []}
          pagination={false}
          locale={{ emptyText: t("pages.deviceDetail.edgeProfiles.empty") }}
        />
      </Drawer>

      <Drawer
        open={!!conflict}
        title={t("pages.deviceDetail.edgeProfiles.conflictTitle", {
          name: conflict?.name,
        })}
        width={720}
        onClose={() => setConflict(null)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setConflict(null)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              disabled={
                !conflict?.targetName.trim() ||
                conflict.targetName.trim() === conflict.name
              }
              loading={adoptMutation.isPending}
              onClick={() =>
                conflict &&
                adoptMutation.mutate({
                  name: conflict.name,
                  targetName: conflict.targetName.trim(),
                })
              }
            >
              {t("pages.deviceDetail.edgeProfiles.adoptAs")}
            </Button>
            <Button
              danger
              loading={adoptMutation.isPending}
              onClick={() =>
                conflict && adoptMutation.mutate({ name: conflict.name, mode: "update" })
              }
            >
              {t("pages.deviceDetail.edgeProfiles.overwrite")}
            </Button>
          </Space>
        }
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.deviceDetail.edgeProfiles.conflictWarn")}
        />
        <Typography.Text type="secondary">
          {t("pages.deviceDetail.edgeProfiles.targetNameLabel")}
        </Typography.Text>
        <Input
          style={{ marginTop: 6 }}
          value={conflict?.targetName ?? ""}
          onChange={(event) =>
            conflict && setConflict({ ...conflict, targetName: event.target.value })
          }
        />
      </Drawer>

      <Drawer
        open={!!result}
        width={720}
        title={
          result?.created
            ? t("pages.deviceDetail.edgeProfiles.resultCreated")
            : result?.updated
              ? t("pages.deviceDetail.edgeProfiles.resultUpdated")
              : t("pages.deviceDetail.edgeProfiles.resultNoop")
        }
        onClose={() => setResult(null)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button type="primary" onClick={() => setResult(null)}>
              {t("common.done")}
            </Button>
          </Space>
        }
      >
        {result && (
          <Space direction="vertical" style={{ width: "100%" }}>
            <Typography.Text>
              {t("pages.deviceDetail.edgeProfiles.resultSummary", {
                name: result.profile_name,
                adopted: result.adopted_count,
              })}
            </Typography.Text>
            {result.skipped.length > 0 && (
              <Alert
                type="info"
                showIcon
                message={t("pages.deviceDetail.edgeProfiles.skippedTitle", {
                  count: result.skipped.length,
                })}
                description={
                  <ul style={{ margin: 0, paddingLeft: 18 }}>
                    {result.skipped.map((s) => (
                      <li key={s.name}>
                        {s.name} ({s.value_type}): {s.reason}
                      </li>
                    ))}
                  </ul>
                }
              />
            )}
          </Space>
        )}
      </Drawer>
    </>
  );
}
