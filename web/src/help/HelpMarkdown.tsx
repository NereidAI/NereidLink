// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// HelpMarkdown renders a help or glossary document from Markdown source.
// GitHub flavored Markdown is supported through remark-gfm. Cross
// references use an explicit custom scheme the author writes by hand:
// `help:glossary/<id>` for a term page and `help:page/<id>` for a catalog
// help page. There is deliberately no automatic term linking: common
// words are ambiguous across contexts (see L-086), so links are placed
// only where an author actually introduces the concept.

import { ReactNode } from "react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { Typography } from "antd";
import type { Components } from "react-markdown";

// helpUrlTransform preserves the custom help: scheme, which
// defaultUrlTransform strips (its safe-protocol allowlist is only
// http/https/mailto/...). Without this, every cross-reference rendered
// as an anchor with an empty href and never routed.
function helpUrlTransform(url: string): string {
  if (url.startsWith("help:")) return url;
  return defaultUrlTransform(url);
}

interface HelpMarkdownProps {
  // source is the raw Markdown text.
  source: string;
  // onHelpLink opens a `help:` target expressed as "page/<id>" or
  // "glossary/<id>". The Drawer swaps documents in place; the help center
  // navigates.
  onHelpLink?: (target: string) => void;
}

// linkComponent renders anchors, routing the custom help: scheme through
// the host callback instead of browser navigation.
function makeLinkComponent(onHelpLink?: (target: string) => void) {
  return function HelpLink({
    href,
    children,
  }: {
    href?: string;
    children?: ReactNode;
  }) {
    if (href?.startsWith("help:")) {
      const target = href.slice("help:".length);
      return (
        <a
          href="#"
          onClick={(event) => {
            event.preventDefault();
            onHelpLink?.(target);
          }}
        >
          {children}
        </a>
      );
    }
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    );
  };
}

// components maps Markdown elements onto AntD typography so rendered
// help documents match the application style.
const baseComponents: Components = {
  h1: ({ children }) => (
    <Typography.Title level={3} style={{ marginTop: 0 }}>{children}</Typography.Title>
  ),
  h2: ({ children }) => <Typography.Title level={4}>{children}</Typography.Title>,
  h3: ({ children }) => <Typography.Title level={5}>{children}</Typography.Title>,
  p: ({ children }) => (
    <Typography.Paragraph style={{ marginBottom: 8 }}>{children}</Typography.Paragraph>
  ),
  ul: ({ children }) => (
    <ul style={{ marginBottom: 8, paddingLeft: 20 }}>{children}</ul>
  ),
  ol: ({ children }) => (
    <ol style={{ marginBottom: 8, paddingLeft: 20 }}>{children}</ol>
  ),
  code: ({ className, children, ...props }) => {
    const isBlock = className?.startsWith("language-");
    if (isBlock) {
      return (
        <pre
          style={{
            background: "#f5f5f5",
            border: "1px solid #f0f0f0",
            borderRadius: 4,
            padding: 12,
            overflow: "auto",
            fontSize: 13,
          }}
        >
          <code className={className} {...props}>{children}</code>
        </pre>
      );
    }
    return (
      <code
        style={{
          background: "#f5f5f5",
          borderRadius: 3,
          padding: "1px 5px",
          fontSize: 13,
        }}
        {...props}
      >
        {children}
      </code>
    );
  },
  table: ({ children }) => (
    <table
      style={{
        borderCollapse: "collapse",
        width: "100%",
        marginBottom: 12,
        fontSize: 13,
      }}
    >
      {children}
    </table>
  ),
  th: ({ children }) => (
    <th
      style={{ border: "1px solid #f0f0f0", padding: "6px 10px", textAlign: "left" }}
    >
      {children}
    </th>
  ),
  td: ({ children }) => (
    <td style={{ border: "1px solid #f0f0f0", padding: "6px 10px" }}>{children}</td>
  ),
};

// HelpMarkdown renders the Markdown document.
export default function HelpMarkdown({ source, onHelpLink }: HelpMarkdownProps) {
  const components: Components = {
    ...baseComponents,
    a: makeLinkComponent(onHelpLink),
  };
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      urlTransform={helpUrlTransform}
      components={components}
    >
      {source}
    </ReactMarkdown>
  );
}
