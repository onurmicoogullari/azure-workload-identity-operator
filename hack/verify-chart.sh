#!/usr/bin/env bash
set -euo pipefail

chart_dir=${HELM_CHART_DIR:-dist/chart}
release_name=${HELM_RELEASE:-azure-workload-identity-operator}
namespace=${HELM_NAMESPACE:-azure-workload-identity-operator-system}
helm_binary=${HELM:-helm}
tmpdir=$(mktemp -d)
rendered=$tmpdir/rendered.yaml
existing_secret_rendered=$tmpdir/existing-secret.yaml
credential_secret_rendered=$tmpdir/credential-secret.yaml
digest_rendered=$tmpdir/digest.yaml
telemetry_rendered=$tmpdir/telemetry.yaml
production_profile_rendered=$tmpdir/production-profile.yaml
single_replica_profile_rendered=$tmpdir/single-replica-profile.yaml
manager_role_rendered=$tmpdir/manager-role.yaml
bundled_webhook_service_rendered=$tmpdir/bundled-webhook-service.yaml
chart_rbac_rules=$tmpdir/chart-rbac-rules.yaml
generated_rbac_rules=$tmpdir/generated-rbac-rules.yaml
normalized_chart_crd=$tmpdir/normalized-chart-crd.yaml
normalized_generated_crd=$tmpdir/normalized-generated-crd.yaml
trap 'rm -rf "$tmpdir"' EXIT

required_values=(
  --set-string azure.tenantId=00000000-0000-0000-0000-000000000000
  --set-string azure.subscriptionId=00000000-0000-0000-0000-000000000000
  --set-string azure.resourceGroupName=rg-chart-test
  --set-string azure.location=swedencentral
)

assert_resource_contains() {
  local manifest=$1
  local kind=$2
  local name=$3
  local expected=$4

  if ! awk -v kind="$kind" -v name="$name" -v expected="$expected" '
    /^---$/ {
      matched_kind = 0
      matched_name = 0
      matched_expected = 0
    }
    $0 == "kind: " kind { matched_kind = 1 }
    $0 == "  name: " name { matched_name = 1 }
    $0 == expected { matched_expected = 1 }
    matched_kind && matched_name && matched_expected { found = 1 }
    END { exit !found }
  ' "$manifest"; then
    echo "$kind/$name is missing expected content: $expected" >&2
    exit 1
  fi
}

render_and_verify_source_sync() {
  "$helm_binary" lint "$chart_dir" "${required_values[@]}"
  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --set-string 'manager.podAnnotations.kubectl\.kubernetes\.io/default-container=overridden' \
    --set-string 'manager.podLabels.app\.kubernetes\.io/name=overridden' \
    --set-string azureWorkloadIdentityWebhook.podLabels.app=overridden \
    --set-string 'azureWorkloadIdentityWebhook.mutatingWebhookAnnotations.cert-manager\.io/inject-ca-from=overridden' \
    "${required_values[@]}" >"$rendered"

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --show-only templates/rbac/manager-role.yaml \
    "${required_values[@]}" >"$manager_role_rendered"
  sed -n '/^rules:/,$p' "$manager_role_rendered" >"$chart_rbac_rules"
  sed -n '/^rules:/,$p' config/rbac/role.yaml >"$generated_rbac_rules"
  if ! diff -u "$generated_rbac_rules" "$chart_rbac_rules"; then
    echo "Helm manager RBAC drifted from generated RBAC" >&2
    exit 1
  fi

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --show-only templates/manager/manager.yaml \
    --set-string azure.credentials.secretRef.name=operator-credentials \
    --set-string azure.credentials.secretRef.keys.clientId=client-id \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id \
    --set-string azure.credentials.secretRef.keys.clientSecret=client-secret \
    "${required_values[@]}" >"$credential_secret_rendered"
  for expected in \
    'name: "operator-credentials"' \
    'key: "client-id"' \
    'key: "tenant-id"' \
    'key: "client-secret"'; do
    grep -Fq "$expected" "$credential_secret_rendered" || {
      echo "manager credential Secret reference is missing expected content: $expected" >&2
      exit 1
    }
  done

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace custom-operator-system \
    --set-string azure.subscriptionId=00000000-0000-0000-0000-000000000000 \
    --set-string azure.resourceGroupName=rg-chart-test \
    --set-string azure.location=swedencentral \
    --set azureWorkloadIdentityWebhook.enabled=false \
    --set-string webhook.certificates.provider=existingSecret \
    --set-string webhook.certificates.existingSecret.name=webhook-tls \
    --set-string 'webhook.certificates.existingSecret.caBundle=-----BEGIN CERTIFICATE-----\nZm9v\n-----END CERTIFICATE-----' \
    >"$existing_secret_rendered"

  release_digest=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --set-string "manager.image.digest=$release_digest" \
    "${required_values[@]}" >"$digest_rendered"
  grep -Fq "image: \"ghcr.io/onurmicoogullari/azure-workload-identity-operator@$release_digest\"" \
    "$digest_rendered" || {
      echo "release image digest was not rendered as the exact operator image reference" >&2
      exit 1
    }

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --set telemetry.tracing.enabled=true \
    --set-string 'manager.extraEnv[0].name=OTEL_EXPORTER_OTLP_ENDPOINT' \
    --set-string 'manager.extraEnv[0].value=https://collector.example:4318' \
    --set-string 'manager.extraVolumes[0].name=operator-otlp-ca' \
    --set-string 'manager.extraVolumes[0].secret.secretName=operator-otlp-ca' \
    --set-string 'manager.extraVolumeMounts[0].name=operator-otlp-ca' \
    --set-string 'manager.extraVolumeMounts[0].mountPath=/var/run/operator-otlp-ca' \
    "${required_values[@]}" >"$telemetry_rendered"

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --values "$chart_dir/values-production.yaml" \
    "${required_values[@]}" >"$production_profile_rendered"

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --values "$chart_dir/values-single-replica.yaml" \
    "${required_values[@]}" >"$single_replica_profile_rendered"
}

assert_template_rejected() {
  local message=$1
  shift

  if "$helm_binary" template invalid "$chart_dir" "$@" >/dev/null 2>&1; then
    echo "$message" >&2
    exit 1
  fi
}

verify_rejected_values() {
  assert_template_rejected "chart unexpectedly accepted an empty Azure subscription ID" \
    --set-string azure.tenantId=tenant \
    --set-string azure.subscriptionId= \
    --set-string azure.resourceGroupName=rg \
    --set-string azure.location=location

  assert_template_rejected "chart unexpectedly accepted an empty credential Secret name" \
    "${required_values[@]}" \
    --set-string azure.credentials.secretRef.name= \
    --set-string azure.credentials.secretRef.keys.clientId=client-id \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id \
    --set-string azure.credentials.secretRef.keys.clientSecret=client-secret

  assert_template_rejected "chart unexpectedly accepted an invalid credential Secret name" \
    "${required_values[@]}" \
    --set-string 'azure.credentials.secretRef.name=invalid name' \
    --set-string azure.credentials.secretRef.keys.clientId=client-id \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id \
    --set-string azure.credentials.secretRef.keys.clientSecret=client-secret

  assert_template_rejected "chart unexpectedly accepted incomplete credential Secret key selectors" \
    "${required_values[@]}" \
    --set-string azure.credentials.secretRef.name=operator-credentials \
    --set-string azure.credentials.secretRef.keys.clientId=client-id \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id

  assert_template_rejected "chart unexpectedly accepted an empty credential Secret key selector" \
    "${required_values[@]}" \
    --set-string azure.credentials.secretRef.name=operator-credentials \
    --set-string azure.credentials.secretRef.keys.clientId= \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id \
    --set-string azure.credentials.secretRef.keys.clientSecret=client-secret

  assert_template_rejected "chart unexpectedly accepted an invalid credential Secret key selector" \
    "${required_values[@]}" \
    --set-string azure.credentials.secretRef.name=operator-credentials \
    --set-string azure.credentials.secretRef.keys.clientId=client/id \
    --set-string azure.credentials.secretRef.keys.tenantId=tenant-id \
    --set-string azure.credentials.secretRef.keys.clientSecret=client-secret

  assert_template_rejected "chart unexpectedly accepted the removed existingSecret value" \
    "${required_values[@]}" \
    --set-string azure.credentials.existingSecret=operator-credentials

  assert_template_rejected "chart unexpectedly accepted a configurable operator webhook port" \
    "${required_values[@]}" \
    --set webhook.port=10443

  assert_template_rejected "chart unexpectedly accepted a configurable Azure webhook Service port" \
    --skip-schema-validation \
    "${required_values[@]}" \
    --set azureWorkloadIdentityWebhook.service.port=8443

  assert_template_rejected "chart unexpectedly accepted a configurable Azure webhook target port" \
    --skip-schema-validation \
    "${required_values[@]}" \
    --set azureWorkloadIdentityWebhook.service.targetPort=10443

  "$helm_binary" template "$release_name" "$chart_dir" \
    --namespace "$namespace" \
    --show-only charts/azureWorkloadIdentityWebhook/templates/azure-wi-webhook-webhook-service-service.yaml \
    "${required_values[@]}" >"$bundled_webhook_service_rendered"
  grep -Fq 'port: 443' "$bundled_webhook_service_rendered"
  grep -Fq 'targetPort: webhook-server' "$bundled_webhook_service_rendered"

  assert_template_rejected "chart unexpectedly accepted a different Azure webhook namespace" \
    --skip-schema-validation \
    "${required_values[@]}" \
    --set-string azureWorkloadIdentityWebhook.namespaceOverride=other-system

  assert_template_rejected "chart unexpectedly enabled the privileged upstream certificate rotator" \
    --skip-schema-validation \
    "${required_values[@]}" \
    --set azureWorkloadIdentityWebhook.disableCertRotation=false

  assert_template_rejected "chart unexpectedly accepted an incomplete existing TLS Secret configuration" \
    --set-string azure.subscriptionId=subscription \
    --set-string azure.resourceGroupName=rg \
    --set-string azure.location=location \
    --set azureWorkloadIdentityWebhook.enabled=false \
    --set-string webhook.certificates.provider=existingSecret

  assert_template_rejected "chart unexpectedly allowed replacement of a fixed manager environment variable" \
    "${required_values[@]}" \
    --set-string 'manager.extraEnv[0].name=POD_UID' \
    --set-string 'manager.extraEnv[0].value=forged'

  assert_template_rejected "chart unexpectedly allowed replacement of the webhook certificate volume" \
    "${required_values[@]}" \
    --set-string 'manager.extraVolumes[0].name=webhook-certs' \
    --set-string 'manager.extraVolumes[0].emptyDir={}'

  assert_template_rejected "chart unexpectedly allowed replacement of the Azure startup scope volume" \
    "${required_values[@]}" \
    --set-string 'manager.extraVolumes[0].name=azure-startup-scope' \
    --set-string 'manager.extraVolumes[0].emptyDir={}'

  assert_template_rejected "chart unexpectedly allowed replacement of the webhook certificate mount" \
    "${required_values[@]}" \
    --set-string 'manager.extraVolumeMounts[0].name=webhook-certs' \
    --set-string 'manager.extraVolumeMounts[0].mountPath=/tmp/replacement'

  assert_template_rejected "chart unexpectedly allowed an overlapping Azure startup scope mount" \
    "${required_values[@]}" \
    --set-string 'manager.extraVolumeMounts[0].name=forged-scope' \
    --set-string 'manager.extraVolumeMounts[0].mountPath=/var/run/azure-workload-identity-operator/startup-scope/location'
}

verify_rendered_contracts() {
  for expected in \
    'kind: ValidatingWebhookConfiguration' \
    'kind: MutatingWebhookConfiguration' \
    'kind: Certificate' \
    'kind: Issuer' \
    'namespace: microsoft-azure-workload-identity-webhook-system' \
    '--disable-cert-rotation=true' \
    '- azure-workload-identity-operator' \
    '- azure-workload-identity-webhook' \
    'cert-manager.io/inject-ca-from: "microsoft-azure-workload-identity-webhook-system/azure-wi-webhook-serving-cert"' \
    'failurePolicy: Fail' \
    'containerPort: 9443' \
    'targetPort: webhook-server' \
    'serviceaccounts/token' \
    'argocd.argoproj.io/sync-options: Prune=false,Delete=false' \
    'argocd.argoproj.io/sync-wave: "-1"' \
    'immutable: true' \
    '--azure-scope-anchor-directory=/var/run/azure-workload-identity-operator/startup-scope' \
    'name: azure-startup-scope' \
    '"helm.sh/resource-policy": keep'; do
    grep -Fq -- "$expected" "$rendered" || {
      echo "rendered chart is missing: $expected" >&2
      exit 1
    }
  done

  if grep -Eq 'runAs(User|Group):' "$rendered"; then
    echo "rendered chart pins a UID or GID and is incompatible with OpenShift restricted SCC" >&2
    exit 1
  fi
  if grep -Fq overridden "$rendered"; then
    echo "rendered chart allowed user values to replace an ownership or selector label" >&2
    exit 1
  fi
  if ! grep -A12 'name: azure-workload-identity-operator-startup-config' "$rendered" | \
    grep -Fq 'helm.sh/resource-policy: keep'; then
    echo "Azure startup scope ConfigMap is not retained across uninstall" >&2
    exit 1
  fi
  if ! grep -A12 'name: azure-workload-identity-operator-startup-config' "$rendered" | \
    grep -Fq 'argocd.argoproj.io/sync-options: Prune=false,Delete=false'; then
    echo "Azure startup scope ConfigMap is not retained by Argo CD" >&2
    exit 1
  fi
  if [[ -e dist/vendor/workload-identity-webhook/templates/azure-wi-webhook-manager-role-role.yaml ||
    -e dist/vendor/workload-identity-webhook/templates/azure-wi-webhook-manager-rolebinding-rolebinding.yaml ||
    -e dist/vendor/workload-identity-webhook/templates/azure-wi-webhook-server-cert-secret.yaml ]]; then
    echo "vendored webhook unexpectedly contains the removed Secret certificate RBAC/resources" >&2
    exit 1
  fi
  if grep -Fq mutatingwebhookconfigurations \
    dist/vendor/workload-identity-webhook/templates/azure-wi-webhook-manager-role-clusterrole.yaml; then
    echo "vendored webhook unexpectedly has permission to modify admission configuration" >&2
    exit 1
  fi

  grep -Fq 'namespace: custom-operator-system' "$existing_secret_rendered"

  for expected in \
    '--telemetry-tracing-enabled' \
    'name: OTEL_EXPORTER_OTLP_ENDPOINT' \
    'value: https://collector.example:4318' \
    'name: operator-otlp-ca' \
    'mountPath: /var/run/operator-otlp-ca'; do
    grep -Fq -- "$expected" "$telemetry_rendered" || {
      echo "telemetry-enabled chart is missing: $expected" >&2
      exit 1
    }
  done
  if grep -Eq '^kind: (Issuer|Certificate)$' "$existing_secret_rendered"; then
    echo "existingSecret mode unexpectedly rendered cert-manager resources" >&2
    exit 1
  fi
  if grep -Fq 'kind: MutatingWebhookConfiguration' "$existing_secret_rendered"; then
    echo "disabling the bundled Azure webhook unexpectedly rendered it" >&2
    exit 1
  fi

  production_pdb_count=$(grep -c '^kind: PodDisruptionBudget$' "$production_profile_rendered" || true)
  if [[ $production_pdb_count -ne 2 ]]; then
    echo "production profile rendered $production_pdb_count PodDisruptionBudgets instead of 2" >&2
    exit 1
  fi
  assert_resource_contains "$production_profile_rendered" Deployment \
    azure-workload-identity-operator-controller-manager '  replicas: 2'
  assert_resource_contains "$production_profile_rendered" Deployment \
    azure-wi-webhook-controller-manager '  replicas: 2'

  single_replica_pdb_count=$(grep -c '^kind: PodDisruptionBudget$' "$single_replica_profile_rendered" || true)
  if [[ $single_replica_pdb_count -ne 0 ]]; then
    echo "single-replica profile unexpectedly rendered $single_replica_pdb_count PodDisruptionBudgets" >&2
    exit 1
  fi
  assert_resource_contains "$single_replica_profile_rendered" Deployment \
    azure-workload-identity-operator-controller-manager '  replicas: 1'
  assert_resource_contains "$single_replica_profile_rendered" Deployment \
    azure-wi-webhook-controller-manager '  replicas: 1'
}

verify_crd_sync() {
  for plural in oidcissuers workloadidentities workloadidentityrecoveries; do
    chart_crd="$chart_dir/templates/crd/${plural}.workloadidentity.azure.micosolutions.se.yaml"
    generated_crd="config/crd/bases/workloadidentity.azure.micosolutions.se_${plural}.yaml"
    sed '1d;$d;/    "helm.sh\/resource-policy": keep/d' "$chart_crd" >"$normalized_chart_crd"
    sed '1d' "$generated_crd" >"$normalized_generated_crd"
    if ! diff -u "$normalized_generated_crd" "$normalized_chart_crd"; then
      echo "Helm CRD template drifted from generated CRD: $plural" >&2
      exit 1
    fi
  done
}

render_and_verify_source_sync
verify_rejected_values
verify_rendered_contracts
verify_crd_sync

echo "Helm chart verification passed"
