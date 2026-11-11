// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ESLint flat configuration for the web client (ADR-0031 lint stage).
//
// This is intentionally a narrow gate: the TypeScript compiler is the source
// of truth for types, so ESLint covers the things tsc does not (React hooks
// rules, accidental non-component exports, unused variables) rather than
// re-litigating the type system. The lint script runs with --max-warnings 0,
// so warn-level rules are hard gate failures too; the levels below document
// severity intent (correctness problems are errors, style/tech-debt signals
// warnings) rather than which findings CI tolerates.

import js from '@eslint/js'
import globals from 'globals'
import tsParser from '@typescript-eslint/parser'
import tsPlugin from '@typescript-eslint/eslint-plugin'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'

export default [
  {
    // Build output and dependencies are never linted.
    ignores: ['dist', 'node_modules'],
  },
  js.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      parser: tsParser,
      ecmaVersion: 2020,
      sourceType: 'module',
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      '@typescript-eslint': tsPlugin,
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...tsPlugin.configs.recommended.rules,
      ...reactHooks.configs.recommended.rules,
      // TypeScript itself verifies identifiers and the automatic JSX runtime
      // does not import React, so the core no-undef rule only produces false
      // positives here ("'React' is not defined"). tsc is the authority.
      'no-undef': 'off',
      // This compiler-era rule flags intentional effects the codebase uses on
      // purpose: the telemetry flash timer, auto-expanding nav on route
      // change and opening a modal from a URL search param. Keep it visible
      // but non-blocking so these deliberate external-sync effects stay green.
      'react-hooks/set-state-in-effect': 'warn',
      // Warn when a file exports both components and other values, which
      // defeats Fast Refresh. Constant exports are allowed.
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
      // Unused variables are a warning, not an error, and names prefixed
      // with underscore are intentionally ignored (placeholder args).
      '@typescript-eslint/no-unused-vars': [
        'warn',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_' },
      ],
      // The codebase uses explicit any in places that bridge untyped MQTT
      // payloads; flag it for cleanup but do not block the build on it.
      '@typescript-eslint/no-explicit-any': 'warn',
    },
  },
  {
    // The application entry bootstraps providers and is never a Fast
    // Refresh boundary, so the component-only-export rule does not apply.
    files: ['src/main.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
]
