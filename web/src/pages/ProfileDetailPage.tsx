// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileDetailPage renders one device profile as an independent route. A
// profile is a vendor-neutral point library plus an optional connector
// template (ADR-0026/ADR-0032); presenting it as a page instead of a tabbed
// modal puts the connector parameters on the top level, so a first-time
// operator and a third-party edge developer see "basic info / connection
// parameters / points" as three obvious sections rather than an editor
// nested inside a dialog. Each section is read-first and saves independently
// through the whole-profile PUT.

import { Alert, Button, Space, Tag } from "antd";
import { ArrowLeftOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router-dom";
import { useMemo, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import { listProfiles } from "../api/profiles";
import { ProfileBasicSection } from "../components/profile/ProfileBasicSection";
import { ProfileConnectorSection } from "../components/profile/ProfileConnectorSection";
import { ProfilePointsSection } from "../components/profile/ProfilePointsSection";
import { ProfileDistributionSection } from "../components/profile/ProfileDistributionSection";

// ProfileDetailPage renders the detail route.
export default function ProfileDetailPage() {
  const { t } = useTranslation();
  const { profileId = "" } = useParams();

  // There is no single-profile endpoint; the list embeds full descriptors
  // and is small/unpaged, so derive the one record from the cached list.
  const profilesQuery = useQuery({
    queryKey: ["profiles"],
    queryFn: listProfiles,
    staleTime: 0,
  });
  const profile = useMemo(
    () => (profilesQuery.data ?? []).find((p) => p.id === profileId),
    [profilesQuery.data, profileId],
  );

  // One open section at a time keeps the page calm; saving/cancel collapses.
  const [openSection, setOpenSection] = useState<"" | "basic" | "connector" | "points">("");
  const toggle = (section: "basic" | "connector" | "points") =>
    setOpenSection((current) => (current === section ? "" : section));
  const close = () => setOpenSection("");

  if (profilesQuery.isError || (!profilesQuery.isLoading && !profile)) {
    return (
      <>
        <BackHeader title={t("pages.profiles.detail.fallback")} />
        <Alert type="error" showIcon message={t("pages.profiles.detail.notFound")} />
      </>
    );
  }

  if (!profile) {
    return <BackHeader title={t("common.loading")} />;
  }

  return (
    <>
      <BackHeader
        title={profile.name}
        id={profile.id}
        tags={
          <Space size={4}>
            {profile.source && (
              <Tag>{t(`pages.profiles.sources.${profile.source}`)}</Tag>
            )}
            {profile.protocol && <Tag color="purple">{profile.protocol}</Tag>}
            {profile.connector_type && <Tag color="geekblue">{profile.connector_type}</Tag>}
            {profile.availability === "discovered" && (
              <Tag>{t("pages.profiles.discovered")}</Tag>
            )}
          </Space>
        }
      />
      <ProfileBasicSection
        profile={profile}
        editing={openSection === "basic"}
        onEdit={() => toggle("basic")}
        onCancel={close}
      />
      <ProfileConnectorSection
        profile={profile}
        editing={openSection === "connector"}
        onEdit={() => toggle("connector")}
        onCancel={close}
      />
      <ProfilePointsSection
        profile={profile}
        editing={openSection === "points"}
        onEdit={() => toggle("points")}
        onCancel={close}
      />
      <ProfileDistributionSection profile={profile} />
    </>
  );
}

// BackHeader renders the standard page header with a back-to-list control.
function BackHeader({
  title,
  id,
  tags,
}: {
  title: string;
  id?: string;
  tags?: ReactNode;
}) {
  const { t } = useTranslation();
  return (
    <>
      <div style={{ marginBottom: 4 }}>
        <Link to="/profiles">
          <Button type="link" size="small" icon={<ArrowLeftOutlined />} style={{ paddingLeft: 0 }}>
            {t("pages.profiles.detail.back")}
          </Button>
        </Link>
      </div>
      <PageHeader title={title} subtitle={id} extra={tags} helpId="profiles" />
    </>
  );
}
