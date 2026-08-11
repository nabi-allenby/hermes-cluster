{{- define "hc.tokensSecretName" -}}
{{- if .Values.connector.existingTokensSecret -}}
{{- .Values.connector.existingTokensSecret -}}
{{- else -}}
{{- printf "%s-hrc-tokens" .Release.Name -}}
{{- end -}}
{{- end -}}

{{- define "hc.connectorURL" -}}
{{- printf "http://%s-hrc:8420" .Release.Name -}}
{{- end -}}

{{- define "hc.wakeBaseURL" -}}
{{- printf "http://%s-hlm:%d" .Release.Name (int .Values.lifecycleManager.listenPort) -}}
{{- end -}}
