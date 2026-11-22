{{- define "sophron.name" -}}sophron{{- end -}}
{{- define "sophron.fullname" -}}
{{- if contains "sophron" .Release.Name -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else -}}{{ printf "%s-sophron" .Release.Name | trunc 63 | trimSuffix "-" }}{{- end -}}
{{- end -}}
{{- define "sophron.labels" -}}
app.kubernetes.io/name: sophron
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}
{{- define "sophron.selector" -}}
app.kubernetes.io/name: sophron
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
{{- define "sophron.authSecret" -}}{{ default (printf "%s-admin" (include "sophron.fullname" .)) .Values.auth.existingSecret }}{{- end -}}
