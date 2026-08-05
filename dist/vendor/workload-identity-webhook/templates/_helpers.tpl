{{- define "workload-identity-webhook.name" -}}
workload-identity-webhook
{{- end }}

{{- define "workload-identity-webhook.namespace" -}}
{{- default .Release.Namespace .Values.namespaceOverride -}}
{{- end }}

{{- define "workload-identity-webhook.podLabels" -}}
{{- with .Values.podLabels }}
{{- with omit . "app" "azure-workload-identity.io/system" "chart" "release" }}
{{- toYaml . | nindent 8 }}
{{- end }}
{{- end }}
{{- end }}
