{{- define "hc.tokensSecretName" -}}
{{- if .Values.connector.existingTokensSecret -}}
{{- .Values.connector.existingTokensSecret -}}
{{- else -}}
{{- printf "%s-relay-connector-tokens" .Release.Name -}}
{{- end -}}
{{- end -}}

{{- define "hc.connectorURL" -}}
{{- printf "http://%s-relay-connector:8420" .Release.Name -}}
{{- end -}}

{{- define "hc.wakeBaseURL" -}}
{{- printf "http://%s-lifecycle-manager:%d" .Release.Name (int .Values.lifecycleManager.listenPort) -}}
{{- end -}}

{{- define "hc.dashboardAuthSecretName" -}}
{{- if .Values.session.serve.existingAuthSecret -}}
{{- .Values.session.serve.existingAuthSecret -}}
{{- else -}}
{{- printf "%s-session-dashboard-auth" .Release.Name -}}
{{- end -}}
{{- end -}}
