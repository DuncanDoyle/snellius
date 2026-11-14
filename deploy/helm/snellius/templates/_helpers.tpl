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
{{/* Nil-safe reads of the secure-mode values; an older release has none of them. */}}
{{- define "snellius.oidcValue" -}}
{{- $oidc := dig "oidc" (dict) (.root.Values.auth | default dict) -}}
{{ dig .key "" $oidc }}
{{- end }}
{{- define "snellius.oidcScopes" -}}
{{ join "," (dig "scopes" (list) (dig "oidc" (dict) (.Values.auth | default dict))) }}
{{- end }}
{{- define "snellius.sessionValue" -}}
{{- $s := dig "session" (dict) (.root.Values.auth | default dict) -}}
{{ dig .key "" $s }}
{{- end }}
{{- define "snellius.cookieSecure" -}}
{{ dig "cookieSecure" true (dig "session" (dict) (.Values.auth | default dict)) }}
{{- end }}
{{- define "snellius.trustedProxyCIDRs" -}}
{{ join "," (dig "trustedProxyCIDRs" (list) (.Values.auth | default dict)) }}
{{- end }}
{{- define "snellius.secretRef" -}}
{{- $oidc := dig "oidc" (dict) (.root.Values.auth | default dict) -}}
{{- $ref := dig .name (dict) $oidc -}}
{{ dig .field "" $ref }}
{{- end }}
{{/* Secure mode needs the issuer, client ID and redirect URL. */}}
{{- define "snellius.requireOIDC" -}}
{{- if eq (include "snellius.authMode" .) "secure" -}}
{{- range $k := list "issuerURL" "clientID" "redirectURL" -}}
{{- if not (include "snellius.oidcValue" (dict "root" $ "key" $k)) -}}
{{- fail (printf "auth.oidc.%s is required when auth.mode is secure" $k) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end }}
