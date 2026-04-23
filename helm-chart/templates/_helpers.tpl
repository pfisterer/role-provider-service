{{/* Base chart name */}}
{{- define "role-provider-service.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* Fully qualified release name */}}
{{- define "role-provider-service.fullname" -}}
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
{{- end }}

{{/* Chart label */}}
{{- define "role-provider-service.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{/* Common labels */}}
{{- define "role-provider-service.labels" -}}
app.kubernetes.io/name: {{ include "role-provider-service.name" . }}
helm.sh/chart: {{ include "role-provider-service.chart" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/* Selector labels */}}
{{- define "role-provider-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "role-provider-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/* Rollout annotations */}}
{{- define "role-provider-service.rolloutAnnotations" -}}
checksum/values: {{ .Values | toJson | sha256sum }}
checksum/all-templates: {{ .Template.BasePath | sha256sum }}
{{- end }}
