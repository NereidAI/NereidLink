// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// HelpCenterPage renders the global help center: a grouped document
// tree (including the standalone Glossary group), client-side full-text
// search, and the selected document or glossary term. It is a plain React
// layout (no AntD Drawer) for persistent browsing.

import { useMemo, useState } from "react";
import { Card, Empty, Input, Menu, Typography } from "antd";
import { SearchOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useParams, useNavigate } from "react-router-dom";
import type { MenuProps } from "antd";

import PageHeader from "../components/PageHeader";
import HelpMarkdown from "../help/HelpMarkdown";
import { getDoc, helpEntries, type HelpGroup } from "../help/docs";
import { getGlossaryDoc, glossaryTerms, parseHelpTarget } from "../help/glossary";

// groupOrder defines the tree order; "glossary" is the special group fed
// from src/docs/*/glossary rather than the help catalog.
const groupOrder: (HelpGroup | "glossary")[] = [
  "monitoring",
  "devices",
  "configuration",
  "operations",
  "integration",
  "system",
  "glossary",
];

// stripMarkdown removes Markdown syntax so search matches readable
// text rather than markup characters.
function stripMarkdown(source: string): string {
  return source
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/[#>*_`~\-[\]()!]/g, " ")
    .replace(/\s+/g, " ")
    .toLowerCase();
}

// CenterDoc is one searchable row in the help center.
interface CenterDoc {
  key: string;
  group: HelpGroup | "glossary";
  order: number;
  title: string;
  source: string;
}

// HelpCenterPage renders the help center.
export default function HelpCenterPage() {
  const { t, i18n } = useTranslation();
  const params = useParams<{ docId?: string; termId?: string }>();
  const navigate = useNavigate();
  const [query, setQuery] = useState("");

  // docs pairs each catalog entry and glossary term with its localized
  // Markdown once per language.
  const docs = useMemo<CenterDoc[]>(() => {
    const pages: CenterDoc[] = helpEntries.map((entry) => {
      const source = getDoc(entry.id, i18n.language);
      return {
        key: entry.id,
        group: entry.group,
        order: entry.order,
        title: source.match(/^#\s+(.+)$/m)?.[1] ?? entry.id,
        source,
      };
    });
    const terms: CenterDoc[] = glossaryTerms(i18n.language).map((term) => ({
      key: `glossary/${term.id}`,
      group: "glossary",
      order: term.order,
      title: term.title,
      source: getGlossaryDoc(term.id, i18n.language),
    }));
    return [...pages, ...terms];
  }, [i18n.language]);

  // results filters documents whose title/body or id contains the query.
  const results = useMemo(() => {
    const needle = query.trim().toLowerCase();
    if (!needle) return docs;
    return docs.filter(
      (doc) =>
        doc.key.includes(needle) ||
        doc.title.toLowerCase().includes(needle) ||
        stripMarkdown(doc.source).includes(needle),
    );
  }, [docs, query]);

  // menuItems builds the grouped tree from the filtered results.
  const menuItems: MenuProps["items"] = groupOrder
    .map((group) => {
      const children = results
        .filter((doc) => doc.group === group)
        .sort((a, b) => a.order - b.order)
        .map((doc) => ({ key: doc.key, label: doc.title }));
      if (children.length === 0) return null;
      return {
        key: `grp-${group}`,
        label: t(`help.groups.${group}`),
        children,
        type: "group" as const,
      };
    })
    .filter(Boolean);

  // selectedKey is the route-selected document, if it survived filtering.
  const routeKey = params.termId ? `glossary/${params.termId}` : params.docId;
  const selected =
    routeKey && results.some((doc) => doc.key === routeKey)
      ? routeKey
      : (results[0]?.key ?? "");

  const selectedDoc = docs.find((doc) => doc.key === selected);
  const isGlossary = selected?.startsWith("glossary/");

  return (
    <>
      <PageHeader
        title={t("nav.help")}
        subtitle={t("help.subtitle")}
        helpId={isGlossary ? undefined : selected}
      />
      <Card>
        <div style={{ display: "flex", gap: 16, minHeight: 480 }}>
          <div style={{ width: 260, flexShrink: 0 }}>
            <Input
              allowClear
              prefix={<SearchOutlined />}
              placeholder={t("help.searchPlaceholder")}
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              style={{ marginBottom: 12 }}
            />
            {results.length > 0 ? (
              <Menu
                mode="inline"
                selectedKeys={selected ? [selected] : []}
                defaultOpenKeys={groupOrder.map((group) => `grp-${group}`)}
                onClick={({ key }) => navigate(`/help/${key}`)}
                items={menuItems}
                style={{ borderInlineEnd: "none" }}
              />
            ) : (
              <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
            )}
          </div>
          <div style={{ flex: 1, minWidth: 0 }}>
            {selectedDoc ? (
              <HelpMarkdown
                source={selectedDoc.source}
                onHelpLink={(target) => {
                  const parsed = parseHelpTarget(target, i18n.language);
                  navigate(
                    parsed.kind === "glossary"
                      ? `/help/glossary/${parsed.id}`
                      : `/help/${parsed.id}`,
                  );
                }}
              />
            ) : (
              <Typography.Text type="secondary">{t("common.noData")}</Typography.Text>
            )}
          </div>
        </div>
      </Card>
    </>
  );
}
