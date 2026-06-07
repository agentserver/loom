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

{{- define "observer.migrationJobName" -}}
{{- if .Values.migration.useHelmHook -}}
{{- printf "%s-migrate" (include "observer.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-migrate-%s" (include "observer.fullname" .) (include "observer.migrationJobSuffix" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "observer.migrationJobSuffix" -}}
{{- $suffix := default .Values.image.tag .Values.migration.jobNameSuffix -}}
{{- if or (not $suffix) (eq $suffix "latest") -}}
{{- $suffix = .Chart.AppVersion -}}
{{- end -}}
{{- $suffix | lower | replace "/" "-" | replace ":" "-" | replace "_" "-" | replace "." "-" | trunc 24 | trimSuffix "-" -}}
{{- end -}}
