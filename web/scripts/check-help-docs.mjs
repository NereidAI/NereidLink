// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// check-help-docs verifies that the help system is internally consistent:
//
//   - every helpId referenced by a page exists in the help catalog;
//   - every catalog entry has both an English and a Chinese Markdown file;
//   - every Markdown file on disk is registered in the catalog;
//   - every glossary term under src/docs/{en,zh}/glossary exists in both
//     languages;
//   - every explicit `help:<id>` Markdown link resolves to a catalog page
//     or glossary term.
//
// The failures this guards against are silent: getDoc falls back to an
// empty string for an unknown id, so a dangling helpId renders a blank
// help drawer with no error anywhere, and a dangling help: link renders a
// term that opens nothing. That is exactly how the license page shipped
// with a helpId that had no document. Run it with `npm run check:help`.

import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const problems = [];

// catalogIds reads the ids registered in the help registry.
function catalogIds() {
  const src = readFileSync(join(root, "src/help/docs.ts"), "utf8");
  return [...src.matchAll(/\{ id: "([^"]+)"/g)].map((match) => match[1]);
}

// termIds lists the glossary Markdown basenames in one language.
function termIds(language) {
  const dir = join(root, "src/docs", language, "glossary");
  return new Set(
    readdirSync(dir).filter((n) => n.endsWith(".md")).map((n) => n.replace(/\.md$/, "")),
  );
}

// helpLinks extracts help-link targets declared in a Markdown file.
// Accepted forms: (help:page/<id>), (help:glossary/<id>) and the legacy
// bare (help:<id>) form.
function helpLinks(file) {
  const src = readFileSync(file, "utf8");
  return [...src.matchAll(/\(help:(?:(page|glossary)\/)?([a-z0-9-]+)\)/g)].map(
    (m) => ({ kind: m[1] ?? "auto", id: m[2] }),
  );
}

// verifyTargets checks every help link in the given Markdown files.
function verifyTargets(files, problems) {
  const pageIDs = new Set(catalog);
  const termIDs = enTerms;
  for (const file of files) {
    for (const { kind, id } of helpLinks(file)) {
      const rel = file.replace(root + "/", "");
      if (kind === "page" && !pageIDs.has(id)) {
        problems.push(`${rel}: help:page/${id} links to an unknown help page`);
      } else if (kind === "glossary" && !termIDs.has(id)) {
        problems.push(`${rel}: help:glossary/${id} links to an unknown term`);
      } else if (kind === "auto" && !pageIDs.has(id) && !termIDs.has(id)) {
        problems.push(`${rel}: help:${id} links to an unknown document`);
      }
    }
  }
}

// listMarkdown yields every page .md path in one language directory
// (glossary subdirectory excluded).
function listMarkdown(language) {
  return readdirSync(join(root, "src/docs", language))
    .filter((n) => n.endsWith(".md"))
    .map((n) => join(root, "src/docs", language, n));
}

// docIds lists the Markdown basenames in one language directory.
function docIds(language) {
  return new Set(
    readdirSync(join(root, "src/docs", language))
      .filter((name) => name.endsWith(".md"))
      .map((name) => name.replace(/\.md$/, "")),
  );
}

const catalog = catalogIds();
const en = docIds("en");
const zh = docIds("zh");
const enTerms = termIds("en");
const zhTerms = termIds("zh");

// Glossary pages must exist in both languages; docIds() reads only the flat
// directory, so terms are checked separately.
for (const id of enTerms) {
  if (!zhTerms.has(id)) problems.push(`glossary term "${id}" missing Chinese page (src/docs/zh/glossary/${id}.md)`);
}
for (const id of zhTerms) {
  if (!enTerms.has(id)) problems.push(`glossary term "${id}" missing English page (src/docs/en/glossary/${id}.md)`);
}

// Every help: link must resolve to a catalog page or glossary term.
const allMarkdown = [
  ...listMarkdown("en"),
  ...listMarkdown("zh"),
  ...[...enTerms].map((id) => join(root, "src/docs/en/glossary", `${id}.md`)),
  ...[...zhTerms].map((id) => join(root, "src/docs/zh/glossary", `${id}.md`)),
];
verifyTargets(allMarkdown, problems);

// Every page helpId must resolve, otherwise the drawer renders empty.
for (const page of readdirSync(join(root, "src/pages"))) {
  if (!page.endsWith(".tsx")) continue;
  const source = readFileSync(join(root, "src/pages", page), "utf8");
  for (const match of source.matchAll(/helpId="([^"]+)"/g)) {
    if (!catalog.includes(match[1])) {
      problems.push(`pages/${page}: helpId "${match[1]}" is not in the help catalog`);
    }
  }
}

// Every catalog entry needs both translations, and nothing may sit on disk
// unregistered: an unregistered file is unreachable from the help center.
for (const id of catalog) {
  if (!en.has(id)) problems.push(`catalog "${id}" has no src/docs/en/${id}.md`);
  if (!zh.has(id)) problems.push(`catalog "${id}" has no src/docs/zh/${id}.md`);
}
for (const id of en) {
  if (!catalog.includes(id)) problems.push(`src/docs/en/${id}.md is not registered`);
}
for (const id of zh) {
  if (!catalog.includes(id)) problems.push(`src/docs/zh/${id}.md is not registered`);
}

if (problems.length > 0) {
  for (const problem of problems) console.error(`help docs: ${problem}`);
  process.exit(1);
}
console.log(
  `help docs: ${catalog.length} documents, ${enTerms.size} glossary terms, all consistent`,
);