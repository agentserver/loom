{{- define "observer.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "observer.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "observer.configSecretName" -}}
{{- if .Values.existingSecret -}}
{{- .Values.existingSecret -}}
{{- else -}}
{{- include "observer.fullname" . -}}
{{- end -}}
{{- end -}}

{{- define "observer.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "observer.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{- define "observer.postgresql.fullname" -}}
{{- printf "%s-postgresql" (include "observer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.minio.fullname" -}}
{{- printf "%s-minio" (include "observer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.minioBucketJobName" -}}
{{- printf "%s-create-bucket" (include "observer.minio.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.migrationJobName" -}}
{{- $base := include "observer.fullname" . -}}
{{- if .Values.migration.useHelmHook -}}
{{- $suffix := "migrate" -}}
{{- $baseMax := sub 62 (len $suffix) | int -}}
{{- printf "%s-%s" ($base | trunc $baseMax | trimSuffix "-") $suffix | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $suffix := printf "migrate-%s" (include "observer.migrationJobSuffix" .) -}}
{{- $baseMax := sub 62 (len $suffix) | int -}}
{{- printf "%s-%s" ($base | trunc $baseMax | trimSuffix "-") $suffix | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "observer.migrationJobSuffix" -}}
{{- $suffix := default .Values.image.tag .Values.migration.jobNameSuffix -}}
{{- if or (not $suffix) (eq $suffix "latest") -}}
{{- $suffix = .Chart.AppVersion -}}
{{- end -}}
{{- $suffix | lower | replace "/" "-" | replace ":" "-" | replace "_" "-" | replace "." "-" | trunc 24 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.retentionCronJobName" -}}
{{- $base := include "observer.fullname" . -}}
{{- $suffix := "retention" -}}
{{- $baseMax := sub 51 (len $suffix) | int -}}
{{- printf "%s-%s" ($base | trunc $baseMax | trimSuffix "-") $suffix | trunc 52 | trimSuffix "-" -}}
{{- end -}}

{{- define "observer.postgresqlWaitInitContainers" -}}
{{- if and (eq .Values.config.store.driver "postgres") .Values.postgresql.wait.enabled }}
initContainers:
  - name: wait-for-postgresql
    image: "{{ .Values.postgresql.image.repository }}:{{ .Values.postgresql.image.tag }}"
    imagePullPolicy: {{ .Values.postgresql.image.pullPolicy }}
    env:
      - name: OBSERVER_POSTGRES_WAIT_DSN
        valueFrom:
          secretKeyRef:
            name: {{ include "observer.configSecretName" . }}
            key: {{ default "database-url" .Values.config.store.postgres.dsnSecretKey }}
    command:
      - /bin/sh
      - -ec
    args:
      - |
        until pg_isready -d "$OBSERVER_POSTGRES_WAIT_DSN"; do
          sleep 2
        done
    {{- with .Values.postgresql.wait.resources }}
    resources:
      {{- toYaml . | nindent 6 }}
    {{- end }}
{{- end }}
{{- end -}}
