{{/* Expand the name of the chart. */}}
{{- define "todo.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Fully qualified app name. */}}
{{- define "todo.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "todo.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Common labels. */}}
{{- define "todo.labels" -}}
helm.sh/chart: {{ include "todo.chart" . }}
{{ include "todo.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "todo.selectorLabels" -}}
app.kubernetes.io/name: {{ include "todo.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "todo.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "todo.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Image reference (tag defaults to the chart appVersion). */}}
{{- define "todo.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end -}}

{{/* Names of generated objects. */}}
{{- define "todo.postgres.fullname" -}}
{{- printf "%s-postgresql" (include "todo.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "todo.authSecretName" -}}
{{- printf "%s-auth" (include "todo.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Resolve the app login password, preserving any previously generated value
across upgrades. Returns the plaintext password (used to populate the Secret).
*/}}
{{- define "todo.authPassword" -}}
{{- if .Values.app.auth.password -}}
{{- .Values.app.auth.password -}}
{{- else -}}
{{- $sec := (lookup "v1" "Secret" .Release.Namespace (include "todo.authSecretName" .)) -}}
{{- if and $sec $sec.data (index $sec.data "password") -}}
{{- index $sec.data "password" | b64dec -}}
{{- else -}}
{{- randAlphaNum 24 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Resolve the internal Postgres password, preserving it across upgrades.
*/}}
{{- define "todo.internalPgPassword" -}}
{{- with .Values.database.postgres.internal.auth.password -}}
{{- . -}}
{{- else -}}
{{- $sec := (lookup "v1" "Secret" .Release.Namespace (include "todo.postgres.fullname" .)) -}}
{{- if and $sec $sec.data (index $sec.data "password") -}}
{{- index $sec.data "password" | b64dec -}}
{{- else -}}
{{- randAlphaNum 24 -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Validate configuration early with clear messages. */}}
{{- define "todo.validate" -}}
{{- $t := .Values.database.type -}}
{{- if not (has $t (list "sqlite" "postgres")) -}}
{{- fail (printf "database.type must be 'sqlite' or 'postgres', got %q" $t) -}}
{{- end -}}
{{- /* "sleepy" is an intentionally undocumented joke theme: accepted, but kept
       out of the message below so it stays an easter egg. */ -}}
{{- if not (has .Values.app.theme (list "coral" "navy" "dark" "sleepy")) -}}
{{- fail (printf "app.theme must be coral|navy|dark, got %q" .Values.app.theme) -}}
{{- end -}}
{{- if eq $t "postgres" -}}
{{- $m := .Values.database.postgres.mode -}}
{{- if not (has $m (list "internal" "external")) -}}
{{- fail (printf "database.postgres.mode must be 'internal' or 'external', got %q" $m) -}}
{{- end -}}
{{- if and (eq $m "external") (not .Values.database.postgres.external.existingSecret) -}}
{{- fail "database.postgres.mode=external requires database.postgres.external.existingSecret" -}}
{{- end -}}
{{- end -}}
{{- end -}}
