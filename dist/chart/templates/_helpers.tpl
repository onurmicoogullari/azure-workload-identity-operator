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
{{- $certificates := .Values.global.webhookCertificates -}}
{{- if eq $certificates.provider "selfManaged" -}}
{{- required "global.webhookCertificates.selfManaged.operator.secretName is required for selfManaged" $certificates.selfManaged.operator.secretName -}}
{{- else if eq $certificates.provider "openShiftServiceCA" -}}
{{- $certificates.openShiftServiceCA.operator.secretName -}}
{{- else -}}
{{- $certificates.certManager.operator.secretName -}}
{{- end -}}
{{- end }}

{{- define "azure-workload-identity-operator.validateManagerExtensions" -}}
{{- $fixedEnv := list "AZURE_TOKEN_CREDENTIALS" "POD_NAME" "POD_UID" "POD_NAMESPACE" "SERVICE_ACCOUNT_NAME" "OPERATOR_VERSION" "AZURE_CLIENT_ID" "AZURE_TENANT_ID" "AZURE_CLIENT_SECRET" -}}
{{- $fixedVolumes := list "azure-startup-scope" "webhook-certs" -}}
{{- $webhookCertificateMountPath := "/tmp/k8s-webhook-server/serving-certs" -}}
{{- $scopeMountPath := "/var/run/azure-workload-identity-operator/startup-scope" -}}
{{- range .Values.manager.extraEnv -}}
{{- if has .name $fixedEnv -}}
{{- fail (printf "manager.extraEnv cannot replace fixed environment variable %q" .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.manager.extraVolumes -}}
{{- if has .name $fixedVolumes -}}
{{- fail (printf "manager.extraVolumes cannot replace fixed volume %q" .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.manager.extraVolumeMounts -}}
{{- if has .name $fixedVolumes -}}
{{- fail (printf "manager.extraVolumeMounts cannot replace fixed volume mount %q" .name) -}}
{{- end -}}
{{- if eq .mountPath $webhookCertificateMountPath -}}
{{- fail "manager.extraVolumeMounts cannot replace the fixed webhook certificate mount path" -}}
{{- end -}}
{{- if or (eq .mountPath $scopeMountPath) (hasPrefix (printf "%s/" $scopeMountPath) .mountPath) -}}
{{- fail "manager.extraVolumeMounts cannot overlap the fixed Azure startup scope mount path" -}}
{{- end -}}
{{- end -}}
{{- end -}}
