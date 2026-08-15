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

{{- define "azure-workload-identity-operator.validateManagerExtensions" -}}
{{- $fixedEnv := list "AZURE_TOKEN_CREDENTIALS" "POD_NAME" "POD_UID" "POD_NAMESPACE" "SERVICE_ACCOUNT_NAME" "OPERATOR_VERSION" "AZURE_CLIENT_ID" "AZURE_TENANT_ID" "AZURE_CLIENT_SECRET" -}}
{{- range .Values.manager.extraEnv -}}
{{- if has .name $fixedEnv -}}
{{- fail (printf "manager.extraEnv cannot replace fixed environment variable %q" .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.manager.extraVolumes -}}
{{- if eq .name "webhook-certs" -}}
{{- fail "manager.extraVolumes cannot replace fixed volume \"webhook-certs\"" -}}
{{- end -}}
{{- end -}}
{{- range .Values.manager.extraVolumeMounts -}}
{{- if eq .name "webhook-certs" -}}
{{- fail "manager.extraVolumeMounts cannot replace fixed volume mount \"webhook-certs\"" -}}
{{- end -}}
{{- if eq .mountPath "/tmp/k8s-webhook-server/serving-certs" -}}
{{- fail "manager.extraVolumeMounts cannot replace the fixed webhook certificate mount path" -}}
{{- end -}}
{{- end -}}
{{- end -}}
