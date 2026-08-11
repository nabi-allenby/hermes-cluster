{{- define "hc.tokensSecretName" -}}
{{- if .Values.connector.existingTokensSecret -}}
{{- .Values.connector.existingTokensSecret -}}
{{- else -}}
{{- printf "%s-connector-tokens" .Release.Name -}}
{{- end -}}
{{- end -}}

{{- define "hc.connectorURL" -}}
{{- printf "http://%s-connector:8420" .Release.Name -}}
{{- end -}}

{{- define "hc.wakeBaseURL" -}}
{{- printf "http://%s-lifecycle-manager:%d" .Release.Name (int .Values.lifecycleManager.listenPort) -}}
{{- end -}}
