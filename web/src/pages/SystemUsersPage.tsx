// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SystemUsersPage is the standalone Users & Roles console (ADR-0030):
// a first-class page for the session tenant's member accounts instead
// of the drawer buried inside the tenant administration page. Member
// administration is delegated to a TENANT_ADMIN inside their own tenant
// in addition to platform admins; other roles see an authorization
// notice.

import { Alert, Tabs } from "antd";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import MembersPanel from "../components/MembersPanel";
import PageHeader from "../components/PageHeader";
import TenantAPIKeysPanel from "../components/TenantAPIKeysPanel";
import { canManageMembers, currentTenantId } from "../api/tenant";
import { authStore } from "../store/authStore";

const tabMembers = "members";
const tabAPIKeys = "api-keys";

const tabHelpDocs: Record<string, string> = {
  [tabMembers]: "system-users",
  [tabAPIKeys]: "integrate-api-keys",
};

// SystemUsersPage renders member and API-key administration for the
// session tenant.
export default function SystemUsersPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  // Subscribe so a session change re-evaluates the delegation gate.
  const user = authStore((state) => state.user);
  const allowed = !!user && canManageMembers();
  const requested = searchParams.get("tab") ?? tabMembers;
  const activeKey = requested === tabAPIKeys ? tabAPIKeys : tabMembers;

  if (!allowed) {
    return (
      <>
        <PageHeader title={t("pages.systemUsers.title")} helpId="system-users" />
        <Alert type="error" showIcon message={t("pages.systemUsers.forbidden")} />
      </>
    );
  }

  const onChange = (key: string) => {
    setSearchParams(key === tabMembers ? {} : { tab: key }, { replace: true });
  };

  return (
    <div>
      <PageHeader
        title={t("pages.systemUsers.title")}
        helpId={tabHelpDocs[activeKey]}
      />
      <Tabs
        activeKey={activeKey}
        onChange={onChange}
        items={[
          {
            key: tabMembers,
            label: t("pages.systemUsers.tabs.members"),
            children: <MembersPanel tenantId={currentTenantId()} />,
          },
          {
            key: tabAPIKeys,
            label: t("pages.systemUsers.tabs.apiKeys"),
            children: <TenantAPIKeysPanel tenantId={currentTenantId()} />,
          },
        ]}
      />
    </div>
  );
}
