{{/*
todo.dbEnv renders the database-related environment variables for the app
container, depending on database.type and (for postgres) the mode. The image's
docker-entrypoint.sh turns these into the config.yaml the app reads, assembling
DATABASE_URL from the individual DB_* parts when no full URL is provided.
*/}}
{{- define "todo.dbEnv" -}}
{{- $db := .Values.database -}}
{{- if eq $db.type "sqlite" }}
- name: DB_DRIVER
  value: sqlite
- name: DATA_DIR
  value: /data
{{- else if eq $db.type "postgres" }}
- name: DB_DRIVER
  value: postgres
{{- if eq $db.postgres.mode "internal" }}
- name: DB_HOST
  value: {{ include "todo.postgres.fullname" . | quote }}
- name: DB_PORT
  value: "5432"
- name: DB_USER
  value: {{ $db.postgres.internal.auth.username | quote }}
- name: DB_NAME
  value: {{ $db.postgres.internal.auth.database | quote }}
- name: DB_SSLMODE
  value: disable
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      {{- if $db.postgres.internal.auth.existingSecret }}
      name: {{ $db.postgres.internal.auth.existingSecret | quote }}
      key: {{ $db.postgres.internal.auth.passwordKey | quote }}
      {{- else }}
      name: {{ include "todo.postgres.fullname" . | quote }}
      key: password
      {{- end }}
{{- else }}
{{- $ext := $db.postgres.external }}
{{- if $ext.uriKey }}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.uriKey | quote }}
{{- else }}
- name: DB_SSLMODE
  value: {{ $ext.sslmode | quote }}
- name: DB_HOST
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.hostKey | quote }}
- name: DB_PORT
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.portKey | quote }}
- name: DB_USER
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.userKey | quote }}
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.passwordKey | quote }}
- name: DB_NAME
  valueFrom:
    secretKeyRef:
      name: {{ $ext.existingSecret | quote }}
      key: {{ $ext.dbnameKey | quote }}
{{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{/* todo.authEnv renders AUTH_USERNAME / AUTH_PASSWORD. */}}
{{- define "todo.authEnv" -}}
- name: AUTH_USERNAME
  value: {{ .Values.app.auth.username | quote }}
{{- if .Values.app.auth.existingSecret }}
- name: AUTH_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.app.auth.existingSecret | quote }}
      key: {{ .Values.app.auth.passwordKey | quote }}
{{- else if .Values.app.auth.password }}
- name: AUTH_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ include "todo.authSecretName" . | quote }}
      key: password
{{- end }}
{{- /* If neither is set, AUTH_PASSWORD is omitted and the app generates one. */ -}}
{{- end -}}
