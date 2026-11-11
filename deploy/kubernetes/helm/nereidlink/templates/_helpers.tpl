{{/*
Copyright (C) 2026 YIQISOFT
SPDX-License-Identifier: Apache-2.0

Shared template helpers for the NereidLink chart.
*/}}

{{- define "nereidlink.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" | lower -}}
{{- end -}}

{{- define "nereidlink.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" | lower -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- include "nereidlink.name" . -}}
{{- end -}}
{{- end -}}

{{- define "nereidlink.labels" -}}
app.kubernetes.io/name: {{ include "nereidlink.name" . }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | lower }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: nereidlink
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "nereidlink.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nereidlink.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/* Fully qualified image for the backend application. */}}
{{- define "nereidlink.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" .Values.image.registry .Values.image.repository $tag -}}
{{- end -}}

{{/* Fully qualified image for the static frontend tier. */}}
{{- define "nereidlink.frontendImage" -}}
{{- $tag := .Values.frontend.image.tag | default .Chart.AppVersion -}}
{{- printf "%s/%s:%s" .Values.frontend.image.registry .Values.frontend.image.repository $tag -}}
{{- end -}}

{{/* Resource base name for the frontend Deployment/Service. It must differ
from the backend fullname: the backend Service selector has no component
discriminator, so matching its name/instance labels would capture frontend
pods too. */}}
{{- define "nereidlink.frontendName" -}}
{{- printf "%s-frontend" (include "nereidlink.fullname" .) -}}
{{- end -}}

{{/* Image for a bundled dependency (postgres/nats). */}}
{{- define "nereidlink.depImage" -}}
{{- printf "%s/%s:%s" .registry .repository .tag -}}
{{- end -}}

{{/* PostgreSQL DSN used by both the migration init container and the app.
The bundled StatefulSet and the CNPG cluster both expose the same
credentials; external mode uses the operator-provided DSN verbatim. CNPG's
-rw service follows the current primary. */}}
{{- define "nereidlink.pgDSN" -}}
{{- if eq .Values.postgresql.mode "external" -}}
{{- .Values.postgresql.externalDSN -}}
{{- else if eq .Values.postgresql.mode "cnpg" -}}
postgres://{{ .Values.postgresql.username }}:{{ .Values.postgresql.password }}@{{ include "nereidlink.fullname" . }}-postgresql-rw:5432/{{ .Values.postgresql.database }}?sslmode=disable
{{- else -}}
postgres://{{ .Values.postgresql.username }}:{{ .Values.postgresql.password }}@{{ include "nereidlink.fullname" . }}-postgresql:5432/{{ .Values.postgresql.database }}?sslmode=disable
{{- end -}}
{{- end -}}

{{/* NATS URL. Bundled service by default, or an external URL (JetStream). */}}
{{- define "nereidlink.natsURL" -}}
{{- if .Values.nats.enabled -}}
nats://{{ include "nereidlink.fullname" . }}-nats:4222
{{- else -}}
{{- .Values.nats.externalURL }}
{{- end -}}
{{- end -}}

{{/* Public browser-facing origin of the console, derived from the ingress
host/TLS. Used to default the OIDC frontend base URL and redirect URI. */}}
{{- define "nereidlink.baseURL" -}}
{{- if and .Values.ingress.enabled .Values.ingress.tls.enabled -}}
https://{{- else -}}http://{{- end -}}{{ .Values.ingress.host }}
{{- end -}}
