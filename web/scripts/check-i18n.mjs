// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// check-i18n guards against two classes of locale regressions:
//
//  1. A key exists in the source English locale but is missing in another
//     shipped locale (or vice versa). tsc types consumers against the English
//     Translation type, so a zh.ts gap compiles fine and renders the raw key.
//  2. A component references a static t("...") key that exists in NEITHER
//     locale (e.g. a namespace typo like pages.deviceDetail.x when the key
//     actually lives under pages.devices). Key-set parity between en/zh alone
//     cannot catch this; the call sites must be scanned too.
//
// Dynamic keys built with template strings (`t(\`a.${x}\`)`) and calls that
// supply an explicit defaultValue are intentionally not checked.
// Run it with `npm run check:i18n`.

import { readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

// walkFiles yields every .ts/.tsx file under dir recursively.
function walkFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (entry === "node_modules" || entry === "dist") continue;
    const st = statSync(full);
    if (st.isDirectory()) out.push(...walkFiles(full));
    else if (/\.(ts|tsx)$/.test(entry)) out.push(full);
  }
  return out;
}

// keyName reads an object literal property key as plain text.
function keyName(name) {
  if (ts.isStringLiteral(name) || ts.isNoSubstitutionTemplateLiteral(name)) {
    return name.text;
  }
  if (ts.isIdentifier(name)) {
    return name.text;
  }
  return null;
}

// flatten walks one locale's top-level object literal and collects dotted
// leaf paths.
function flatten(file) {
  const src = readFileSync(file, "utf8");
  const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true);
  const keys = new Set();

  function visitObject(node, path) {
    for (const prop of node.properties) {
      if (!ts.isPropertyAssignment(prop)) continue;
      const key = keyName(prop.name);
      if (!key) continue;
      const next = [...path, key];
      if (ts.isObjectLiteralExpression(prop.initializer)) {
        visitObject(prop.initializer, next);
      } else {
        keys.add(next.join("."));
      }
    }
  }

  function visit(node) {
    // Both `const en = {...}` and any exported/declared top-level object are
    // caught; locale modules export one object.
    if (
      ts.isVariableDeclaration(node) &&
      ts.isObjectLiteralExpression(node.initializer)
    ) {
      visitObject(node.initializer, []);
    }
    ts.forEachChild(node, visit);
  }
  visit(sf);
  return keys;
}

const locales = [
  ["en", join(root, "src/locales/en.ts")],
  ["zh", join(root, "src/locales/zh.ts")],
];
const enFile = locales[0][1];
const en = flatten(enFile);
let failed = false;
for (const [code, file] of locales.slice(1)) {
  const other = flatten(file);
  const missing = [...en].filter((k) => !other.has(k)).sort();
  const extra = [...other].filter((k) => !en.has(k)).sort();
  console.log(`${code}: ${other.size} keys (en ${en.size})`);
  if (missing.length > 0) {
    failed = true;
    console.error(`Missing in ${code}.ts:\n${missing.map((k) => `  ${k}`).join("\n")}`);
  }
  if (extra.length > 0) {
    failed = true;
    console.error(`Extra keys in ${code}.ts (not in en.ts):\n${extra.map((k) => `  ${k}`).join("\n")}`);
  }
}
if (failed) process.exit(1);
console.log("i18n keys in sync");

// referencedKeys scans call sites for static t("a.b.c") strings and returns
// the set of keys referenced. It intentionally accepts:
//   - identifiers named t/translate/i18n.t (the useTranslation hook);
//   - only plain string literals that look like dotted keys;
// and skips template-literal/dynamic keys and calls passing defaultValue.
function referencedKeys() {
  const refs = new Map(); // key -> first source file
  const files = walkFiles(join(root, "src")).filter(
    (f) => /\.(ts|tsx)$/.test(f) && !f.endsWith("locales/en.ts") && !f.endsWith("locales/zh.ts"),
  );
  for (const file of files) {
    const src = readFileSync(file, "utf8");
    const sf = ts.createSourceFile(file, src, ts.ScriptTarget.Latest, true);
    const visit = (node) => {
      if (ts.isCallExpression(node)) {
        const isT =
          (ts.isIdentifier(node.expression) && ["t", "translate"].includes(node.expression.text)) ||
          (ts.isPropertyAccessExpression(node.expression) &&
            ts.isIdentifier(node.expression.name) &&
            node.expression.name.text === "t");
        const arg = node.arguments[0];
        const hasDefault =
          node.arguments[1] &&
          ts.isObjectLiteralExpression(node.arguments[1]) &&
          node.arguments[1].properties.some(
            (p) =>
              ts.isPropertyAssignment(p) &&
              ts.isIdentifier(p.name) &&
              p.name.text === "defaultValue",
          );
        if (isT && arg && ts.isStringLiteral(arg) && !hasDefault) {
          const key = arg.text;
          if (key.includes(".") && !refs.has(key)) refs.set(key, file.replace(root + "/", ""));
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(sf);
  }
  return refs;
}

// Dynamic call sites are normal, but a static key absent from BOTH shipped
// locales is always a bug (namespace typo, moved key, forgotten addition).
const refs = referencedKeys();
const dangling = [...refs.entries()]
  .filter(([key]) => !en.has(key))
  .sort((a, b) => a[0].localeCompare(b[0]));
if (dangling.length > 0) {
  console.error(
    `Static t() keys missing from locale files (${dangling.length}):\n` +
      dangling.map(([k, f]) => `  ${k}  (${f})`).join("\n"),
  );
  process.exit(1);
}
console.log(`static call-site keys: ${refs.size}, all defined`);
