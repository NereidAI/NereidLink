// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// HelpDrawer is the single global Drawer that shows a page help
// document or, after following a term link inside it, a glossary page.
// It is mounted once in the application shell and reacts to the help
// store.

import { Drawer, Space, Button } from "antd";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import HelpMarkdown from "./HelpMarkdown";
import { getDoc } from "./docs";
import { getGlossaryDoc, parseHelpTarget } from "./glossary";
import { useHelpStore } from "./helpStore";

// HelpDrawer renders the global page help Drawer.
export default function HelpDrawer() {
  const { i18n, t } = useTranslation();
  const docID = useHelpStore((state) => state.docID);
  const termID = useHelpStore((state) => state.termID);
  const open = useHelpStore((state) => state.open);
  const showTerm = useHelpStore((state) => state.showTerm);
  const close = useHelpStore((state) => state.close);

  const viewingTerm = Boolean(termID);
  const currentID = termID ?? docID;
  const source = currentID
    ? viewingTerm
      ? getGlossaryDoc(currentID, i18n.language)
      : getDoc(currentID, i18n.language)
    : "";

  // onLink routes a help: target to a page doc or a glossary term.
  const onLink = (target: string) => {
    const parsed = parseHelpTarget(target, i18n.language);
    if (parsed.kind === "glossary") showTerm(parsed.id);
    else open(parsed.id);
  };

  return (
    <Drawer
      width={520}
      open={Boolean(docID)}
      onClose={close}
      title={t("nav.help")}
      extra={
        <Space>
          <Link
            to={
              viewingTerm
                ? `/help/glossary/${currentID}`
                : `/help/${currentID ?? ""}`
            }
            onClick={close}
          >
            <Button type="link" size="small">{t("nav.help")}</Button>
          </Link>
        </Space>
      }
    >
      {currentID ? (
        <HelpMarkdown source={source} onHelpLink={onLink} />
      ) : null}
    </Drawer>
  );
}
