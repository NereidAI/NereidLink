<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Contributing to NereidLink

Thank you for contributing to NereidLink. Keep changes focused, preserve the
existing architecture, and include the checks and documentation relevant to
the change.

## Development environment

The backend requires Go 1.27. The web console requires Node.js 24 or newer.
Container builds require Docker with BuildKit; the default local image targets
build both `linux/amd64` and `linux/arm64` and therefore require a Docker setup
that supports loading multi-platform images.

Install the locked frontend dependencies from the public npm registry:

```bash
cd web
npm ci --no-audit --no-fund
```

Site-specific proxies and registry mirrors belong in the git-ignored
`local.mk`, not in tracked files.

## Verification

Format Go code and run the backend checks from the repository root:

```bash
gofmt -w cmd internal
make test
make lint
```

Run the frontend checks from `web/`:

```bash
cd web
npm run check
npm run lint
npm run build
```

`npm run check` validates both the bilingual help structure and locale-key
consistency. Vite's production build does not replace these checks.

Build the two publication images locally when changing their Dockerfiles,
build arguments, or embedded content:

```bash
make image
make image-frontend
```

These targets build and load images only; they do not push them. Use
`PLATFORMS=linux/amd64` for an explicitly narrowed local verification build.

## Language and product documentation

Use English for runtime code, comments, API messages, tests, commit messages,
and ordinary documentation. Do not hardcode user-facing frontend strings.
Add every locale key to both `web/src/locales/en.ts` and
`web/src/locales/zh.ts`, and keep translation calls consistent with the
existing `t(...)` patterns.

Operator help is a bilingual product feature. Update the matching documents
in both `web/src/docs/en/` and `web/src/docs/zh/`, register new help IDs in
`web/src/help/docs.ts`, and wire the relevant page, tab, or help link to the
entry point. Chinese is retained in locale resources and Chinese help
content; it is not an exception for runtime code or ordinary documentation.

REST, WebSocket, or wire-contract changes must update the API and protocol
documentation and their bilingual integration help in the same change.

## SPDX headers

First-party source and text files must carry an SPDX header near the start of
the file:

```text
Copyright (C) 2026 YIQISOFT
SPDX-License-Identifier: Apache-2.0
```

Use the file format's normal comment syntax. Markdown uses a first-line HTML
comment containing both fields. Existing files provide examples for Go,
TypeScript, YAML, shell, HTML, SVG, and Markdown.

Do not add headers to files whose format or upstream ownership makes them
inappropriate:

- `LICENSE`, `NOTICE`, and `THIRD_PARTY_NOTICES`;
- JSON files, dependency lockfiles, `go.mod`, and `go.sum`;
- PEM files, including `configs/license/public.pem`;
- binary assets such as PNG images;
- the vendored `docs/api/swaggerui/` tree, which retains its upstream license.

## Vendored code and SDK mirrors

Do not treat `docs/api/swaggerui/` as first-party application code. It is a
vendored upstream asset; preserve its license and update it through a
deliberate upstream sync rather than unrelated local edits.

The authoritative device SDK source is `sdk/device/go/` in this repository.
The NereidEdge copy under `third_party/nereidlink` is a downstream mirror. Do
not independently modify the mirror and the authoritative copy; make SDK
changes here and use the suite's synchronization process.

## Commits and the DCO

NereidLink uses the Developer Certificate of Origin. Every commit must include
a `Signed-off-by` trailer matching its author. Configure your real name and
email, then create commits with:

```bash
git commit -s
```

The pull-request DCO workflow checks every non-merge commit. Keep generated
files, dependency updates, legal notices, and functional changes in focused
commits so their review and verification evidence remain clear.
