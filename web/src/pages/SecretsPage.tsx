// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretsPage is the combined secrets workspace: the tenant secret vault
// and the edge secret type descriptor catalog live as two tabs (ADR-0034).
// The active tab is reflected in the ?tab= search parameter so it can be
// deep-linked, and the page-header help target follows the active tab.

import { Tabs } from "antd";
import { useTranslation } from "react-i18next";
import { useSearchParams } from "react-router-dom";

import PageHeader from "../components/PageHeader";
import SecretsPanel from "../components/SecretsPanel";
import SecretTypesPanel from "../components/SecretTypesPanel";

// Tab keys used in the ?tab= search parameter.
const tabSecrets = "secrets";
const tabTypes = "types";

// SecretsPage renders the combined vault/type-catalog workspace.
export default function SecretsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const requested = searchParams.get("tab") ?? tabSecrets;
  const activeKey = requested === tabTypes ? tabTypes : tabSecrets;

  const onChange = (key: string) => {
    setSearchParams(key === tabSecrets ? {} : { tab: key }, { replace: true });
  };

  return (
    <div>
      <PageHeader
        title={t("nav.secrets")}
        helpId={activeKey === tabTypes ? "edge-secret-types" : "edge-secrets"}
      />
      <Tabs
        activeKey={activeKey}
        onChange={onChange}
        items={[
          {
            key: tabSecrets,
            label: t("pages.secrets.tabs.secrets"),
            children: <SecretsPanel />,
          },
          {
            key: tabTypes,
            label: t("pages.secrets.tabs.types"),
            children: <SecretTypesPanel />,
          },
        ]}
      />
    </div>
  );
}
