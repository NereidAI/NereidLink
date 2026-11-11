// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// PageStub is the temporary Stage 1 placeholder used by routes whose full
// content ships in later stages. It keeps routing and navigation complete
// without pretending the feature is implemented.

import { Card, Empty } from "antd";

import PageHeader from "../components/PageHeader";

interface PageStubProps {
  title: string;
  subtitle?: string;
}

// PageStub renders a heading and an empty body card.
export default function PageStub({ title, subtitle }: PageStubProps) {
  return (
    <>
      <PageHeader title={title} subtitle={subtitle} />
      <Card>
        <Empty description={false} />
      </Card>
    </>
  );
}
