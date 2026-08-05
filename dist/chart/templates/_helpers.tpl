{{/* Stable, release-independent resource names enforce one release per cluster. */}}
{{- define "azure-workload-identity-operator.name" -}}
azure-workload-identity-operator
{{- end }}

{{- define "azure-workload-identity-operator.resourceName" -}}
{{- printf "%s-%s" (include "azure-workload-identity-operator.name" .context) .suffix -}}
{{- end }}

{{- define "azure-workload-identity-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "azure-workload-identity-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "azure-workload-identity-operator.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "azure-workload-identity-operator.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{- define "azure-workload-identity-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
azure-workload-identity-operator-controller-manager
{{- else -}}
{{- required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name -}}
{{- end -}}
{{- end }}

{{- define "azure-workload-identity-operator.webhookSecretName" -}}
{{- if eq .Values.webhook.certificates.provider "existingSecret" -}}
{{- required "webhook.certificates.existingSecret.name is required for existingSecret" .Values.webhook.certificates.existingSecret.name -}}
{{- else -}}
{{- .Values.webhook.certificates.certManager.secretName -}}
{{- end -}}
{{- end }}
