{{/*
Chart name, truncated and DNS-1123-safe.
*/}}
{{- define "cloudflare-exporter.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Release-qualified full name, matching Helm's standard chart scaffold convention.
*/}}
{{- define "cloudflare-exporter.fullname" -}}
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

{{/*
Common labels applied to every object.
*/}}
{{- define "cloudflare-exporter.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{ include "cloudflare-exporter.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Labels used to select the Deployment's Pods; must stay stable across upgrades.
*/}}
{{- define "cloudflare-exporter.selectorLabels" -}}
app.kubernetes.io/name: {{ include "cloudflare-exporter.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Name of the Secret holding CLOUDFLARE_API_TOKEN, whichever apiToken.* mode is in use.
*/}}
{{- define "cloudflare-exporter.secretName" -}}
{{- .Values.apiToken.existingSecret | default (printf "%s-token" (include "cloudflare-exporter.fullname" .)) -}}
{{- end -}}

{{/*
Name of the ServiceAccount to use, whichever serviceAccount.* mode is in use.
*/}}
{{- define "cloudflare-exporter.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "cloudflare-exporter.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}
