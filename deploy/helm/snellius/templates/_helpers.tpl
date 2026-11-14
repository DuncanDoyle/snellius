{{- define "snellius.fullname" -}}{{ printf "%s-snellius" .Release.Name | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "snellius.labels" -}}
app.kubernetes.io/name: snellius
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
{{- define "snellius.selector" -}}
app.kubernetes.io/name: snellius
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
{{- define "snellius.sa" -}}
{{- if and (not .Values.serviceAccount.create) (not .Values.serviceAccount.name) -}}
{{- fail "serviceAccount.name is required when serviceAccount.create is false" -}}
{{- end -}}
{{ default (include "snellius.fullname" .) .Values.serviceAccount.name }}
{{- end }}
{{/* Nil-safe: --reuse-values from an older release has no auth or audit keys. */}}
{{- define "snellius.authMode" -}}
{{ dig "mode" "local" (.Values.auth | default dict) }}
{{- end }}
{{- define "snellius.allowedHosts" -}}
{{ join "," (dig "allowedHosts" (list) (.Values.auth | default dict)) }}
{{- end }}
{{- define "snellius.auditLevels" -}}
{{ join "," (dig "levels" (list "security" "agent" "reads" "summaries") (.Values.audit | default dict)) }}
{{- end }}
