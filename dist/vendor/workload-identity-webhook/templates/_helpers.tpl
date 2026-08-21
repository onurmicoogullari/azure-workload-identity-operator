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

{{- define "workload-identity-webhook.certificateSecretName" -}}
{{- $certificates := .Values.global.webhookCertificates -}}
{{- if eq $certificates.provider "selfManaged" -}}
{{- required "global.webhookCertificates.selfManaged.azureWorkloadIdentity.secretName is required for selfManaged" $certificates.selfManaged.azureWorkloadIdentity.secretName -}}
{{- else if eq $certificates.provider "openShiftServiceCA" -}}
{{- $certificates.openShiftServiceCA.azureWorkloadIdentity.secretName -}}
{{- else -}}
{{- $certificates.certManager.azureWorkloadIdentity.secretName -}}
{{- end -}}
{{- end }}
