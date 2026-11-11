// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// NotFoundPage is the catch-all route rendered for unknown paths.

import { Button, Card, Result } from "antd";
import { useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";

// NotFoundPage renders a 404 result with a link back to the overview.
export default function NotFoundPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Card>
      <Result
        status="404"
        title="404"
        subTitle={t("pages.notFound.title")}
        extra={
          <Button type="primary" onClick={() => navigate("/overview")}>
            {t("pages.notFound.home")}
          </Button>
        }
      />
    </Card>
  );
}
