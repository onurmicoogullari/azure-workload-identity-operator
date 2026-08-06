#!/usr/bin/env bash
set -euo pipefail
shopt -s extglob
umask 077

usage() {
  cat <<'EOF'
OpenShift e2e smoke test for OIDCIssuer + WorkloadIdentity + Azure Key Vault.

Uses the current kubeconfig/oc session and active Azure CLI account. By default,
the Azure CLI identity creates and later deletes an ephemeral application and
Service Principal for the in-cluster operator.

Optional env:
  AZURE_CLIENT_ID                             existing Service Principal fallback; set the complete credential trio
  AZURE_TENANT_ID                             existing Service Principal fallback; set the complete credential trio
  AZURE_CLIENT_SECRET                         existing Service Principal fallback; set the complete credential trio
  AZURE_SUBSCRIPTION_ID                       default: current az account
  AZURE_LOCATION                              default: swedencentral
  INSTALL_CERT_MANAGER                        default: true
  CERT_MANAGER_VERSION                        default: v1.21.1
  CERT_MANAGER_NAMESPACE                      default: cert-manager
  CERT_MANAGER_RELEASE                        default: cert-manager
  OPERATOR_NAMESPACE                          default: azure-workload-identity-operator-system
  OPERATOR_RELEASE                            default: azure-workload-identity-operator
  OPERATOR_IMAGE_NAME                         default: azure-workload-identity-operator
  OPERATOR_CANDIDATE_RUN_ID                   downloads and validates the exact Release Candidate bundle
  OPERATOR_IMAGE_REPOSITORY                   optional candidate metadata cross-check
  OPERATOR_IMAGE_DIGEST                       optional candidate metadata cross-check
  OPERATOR_CANDIDATE_COMMIT                   optional candidate metadata cross-check
  OPERATOR_CREDENTIALS_SECRET                 default: azure-workload-identity-operator-azure-credentials
  OPERATOR_OIDC_ISSUER_REFRESH_INTERVAL       default: 1m
  OPERATOR_WORKLOAD_IDENTITY_REFRESH_INTERVAL default: 1m
  ENSURE_KEY_VAULT                            default: true; false uses and retains an existing KEY_VAULT_NAME
  ENABLE_KEY_VAULT_RBAC                       default: true
  AZURE_RESOURCE_GROUP_NAME                   default: rg-azwi-crc-platform-test-<run-id>
  AZURE_KEY_VAULT_RESOURCE_GROUP_NAME         default: rg-azwi-crc-kv-test-<run-id>
  AZURE_STORAGE_ACCOUNT_NAME                  default: stazwicrc<run-id>
  AZURE_BLOB_CONTAINER_NAME                   default: oidc
  ASSIGN_OIDC_STORAGE_BLOB_ROLE               default: true
  OIDC_STORAGE_BLOB_ROLE                      default: Storage Blob Data Contributor
  AZURE_CLI_PRINCIPAL_ID                      default: object ID resolved from the active Azure CLI identity
  AZURE_CLI_PRINCIPAL_TYPE                    default: type resolved from the active Azure CLI identity
  OPERATOR_AZURE_PRINCIPAL_ID                 default: object ID resolved from AZURE_CLIENT_ID
  OPERATOR_AZURE_RESOURCE_ROLE                default: Contributor for an ephemeral Service Principal
  AZURE_USER_ASSIGNED_IDENTITY_NAME           default: id-azwi-crc-test (suffix; Azure name is NAMESPACE-value)
  AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME    default: fidc-azwi-crc-test
  KEY_VAULT_NAME                              default: kv-azwi-<run-id>
  KEY_VAULT_SECRET_NAME                       default: test-secret
  KEY_VAULT_SECRET_VALUE                      default: generated smoke test value
  UPLOAD_KEYVAULT_SECRET                      default: true
  ASSIGN_KEYVAULT_SECRET_WRITER_ROLE          default: true
  KEY_VAULT_SECRET_WRITER_ROLE                default: Key Vault Secrets Officer
  NAMESPACE                                   default: azwi-crc-test
  WORKLOAD_IDENTITY_NAME                      default: azwi-crc-test
  SERVICE_ACCOUNT_NAME                        default: WORKLOAD_IDENTITY_NAME
  SIGNING_KEY_SECRET_NAMESPACE                default: openshift-kube-apiserver
  SIGNING_KEY_SECRET_NAME                     default: bound-service-account-signing-key
  SIGNING_KEY_SECRET_KEY                      default: service-account.pub
  VERIFY_SIGNING_KEY_ROTATION                 default: true
  VERIFY_OIDC_ISSUER_PERIODIC_REFRESH         default: VERIFY_SIGNING_KEY_ROTATION
  RETIRING_SIGNING_KEY_SECRET_NAMESPACE       default: NAMESPACE
  RETIRING_SIGNING_KEY_SECRET_NAME            default: azwi-crc-retiring-signing-key
  RETIRING_SIGNING_KEY_SECRET_KEY             default: service-account.pub
  OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER     default: true
  OIDC_ISSUER_DELETION_POLICY                 default: Delete
  WORKLOAD_IDENTITY_DELETION_POLICY           default: Delete
  VERIFY_WORKLOAD_IDENTITY_SERVICE_ACCOUNT_RECREATION default: true
  VERIFY_WORKLOAD_IDENTITY_CONFLICTS          default: true
  VERIFY_WORKLOAD_IDENTITY_AZURE_DRIFT        default: true
  VERIFY_WORKLOAD_IDENTITY_CONTROLLED_RECOVERY default: true
  IMAGE_NAME                                  default: azwi-crc-test
  JOB_NAME                                    default: azwi-crc-test
  WAIT_TIMEOUT                                default: 10m
  OPENSHIFT_API_SERVER_ROLLOUT_TIMEOUT        default: WAIT_TIMEOUT
  NO_COLOR                                    disable colored log operation prefixes
  FORCE_COLOR                                 set to 1 to force colored log operation prefixes
  AZURE_RBAC_PROPAGATION_TIMEOUT              default: 5m
  KEY_VAULT_PURGE_TIMEOUT                     default: 20m
  ASSIGN_KEYVAULT_ROLE                        default: true
  KEY_VAULT_ROLE                              default: Key Vault Secrets User
  KEY_VAULT_READ_TIMEOUT_SECONDS              default: 300

Example:
  ./test/e2e/openshift/e2e-test.sh

The operator creates one shared Azure resource group for OIDCIssuer storage and
WorkloadIdentity resources. The script verifies the operator retains it and
deletes it manually during cleanup. Key Vault uses a separate script-created
group.
EOF
}

while (($# > 0)); do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

log() {
  local operation=${1:-INFO}
  local message
  local prefix
  ensure_step_started
  if (($# > 0)); then
    shift
  fi
  message=${*:-}
  prefix=$(log_prefix "$operation")

  while IFS= read -r line; do
    printf '   %b %s\n' "$prefix" "$line" >&3
  done <<<"$message"
}

ensure_step_started() {
  if [[ -z ${current_script_step:-} ]]; then
    begin_step 1
  fi
}

begin_step() {
  local step=$1
  local description

  if [[ ${current_script_step:-} == "$step" ]]; then
    return
  fi

  description=$(script_step_description "$step")
  current_script_step=$step
  printf '\n%s. %s\n' "$step" "$description" >&3
}

script_step_description() {
  case "$1" in
    1) printf 'Create the ephemeral operator identity, verify test-owned Azure resources are absent, and install cert-manager.' ;;
    2) printf 'Build the operator image in the internal OpenShift registry.' ;;
    3) printf 'Install the complete operator Helm release.' ;;
    4) printf 'Verify packaged deployments, certificates, and API-server webhooks.' ;;
    5) printf 'Create the test namespace.' ;;
    6) printf 'Create OIDCIssuer/default.' ;;
    7) printf 'Grant blob upload access for OIDC document publishing.' ;;
    8) printf 'Wait for OIDCIssuer/default Ready and published issuer URL.' ;;
    9) printf 'Verify previous OpenShift service-account issuer was captured.' ;;
    10) printf 'Add a test retiring signing key and verify JWKS has active + retiring keys.' ;;
    11) printf 'Wait for OpenShift to use the new issuer and mint tokens with that iss.' ;;
    12) printf 'Replace the retiring key Secret only, then verify periodic refresh republishes it.' ;;
    13) printf 'Create Key Vault.' ;;
    14) printf 'Create WorkloadIdentity and verify immutable naming and ServiceAccount provenance across recreation.' ;;
    15) printf 'Verify ServiceAccount and Azure identity ownership conflict handling.' ;;
    16) printf 'Upload a real Key Vault secret and assign read access.' ;;
    17) printf 'Build and run the OpenShift Job.' ;;
    18) printf 'Verify the Job reads the Key Vault secret using workload identity.' ;;
    19) printf 'Mutate Azure federated credential and verify WorkloadIdentity periodic reconcile repairs it.' ;;
    20) printf 'Retain and recreate WorkloadIdentity, then verify controlled recovery and its constraints.' ;;
    21) printf 'Verify unsafe OIDCIssuer deletion is rejected while WorkloadIdentity exists.' ;;
    22) printf 'During cleanup, verify deletion is also rejected while OpenShift still references the issuer.' ;;
    23) printf 'Restore OpenShift issuer, delete resources, verify the shared group is retained, and clean it up.' ;;
    *) printf 'Run OpenShift e2e step.' ;;
  esac
}

log_prefix() {
  local operation=$1
  local padded
  padded=$(printf '%-8s' "${operation}:")

  if ! should_color_logs; then
    printf '%s' "$padded"
    return
  fi

  case "$operation" in
    ERROR|FAIL) printf '\033[31;1m%s\033[0m' "$padded" ;;
    PASS) printf '\033[32;1m%s\033[0m' "$padded" ;;
    READY|VERIFY) printf '\033[32;1m%s\033[0m' "$padded" ;;
    WATCH|RETRY) printf '\033[34;1m%s\033[0m' "$padded" ;;
    INSTALL|APPLY|CREATE|UPDATE|PATCH|UPLOAD|BUILD|RUN) printf '\033[36;1m%s\033[0m' "$padded" ;;
    DELETE) printf '\033[35;1m%s\033[0m' "$padded" ;;
    SKIP|CONFIG|CAPTURE|READ) printf '\033[33;1m%s\033[0m' "$padded" ;;
    *) printf '\033[36;1m%s\033[0m' "$padded" ;;
  esac
}

should_color_logs() {
  if [[ -n ${NO_COLOR:-} ]]; then
    return 1
  fi
  if [[ ${FORCE_COLOR:-} == "1" || ${CLICOLOR_FORCE:-} == "1" ]]; then
    return 0
  fi
  [[ -t 3 && ${TERM:-} != "dumb" ]]
}

indent_output() {
  local indent="            "
  local columns
  local content_width
  local folded_line

  columns=$(tput cols 2>/dev/null || printf '120')
  content_width=$((columns - ${#indent}))
  if ((content_width < 20)); then
    content_width=20
  fi

  while IFS= read -r line; do
    if [[ -z $line ]]; then
      printf '%s\n' "$indent"
      continue
    fi
    while IFS= read -r folded_line; do
      printf '%s%s\n' "$indent" "$folded_line"
    done < <(printf '%s\n' "$line" | fold -s -w "$content_width")
  done
}

current_script_step=""
exec 3>&2
exec > >(indent_output) 2> >(indent_output >&3)

die() {
  primary_failure=$*
  log ERROR "$*"
  exit 1
}

need() {
  command -v "$1" >/dev/null || die "missing required command: $1"
}

assert_crc_cluster_target() {
  local api_server
  local current_user
  local operator_conditions
  local name
  local available
  local progressing
  local degraded

  api_server=$(oc config view --minify -o jsonpath='{.clusters[0].cluster.server}')
  if [[ $api_server != "https://api.crc.testing:6443" ]]; then
    die "refusing to run against Kubernetes API server ${api_server:-<empty>}; expected https://api.crc.testing:6443"
  fi

  current_user=$(oc whoami)
  if [[ $current_user != "kubeadmin" ]]; then
    die "refusing to run as ${current_user:-<unknown>}; authenticate to CRC as kubeadmin"
  fi

  operator_conditions=$(oc get clusteroperators \
    -o go-template='{{range .items}}{{.metadata.name}}{{"\t"}}{{range .status.conditions}}{{if eq .type "Available"}}{{.status}}{{end}}{{end}}{{"\t"}}{{range .status.conditions}}{{if eq .type "Progressing"}}{{.status}}{{end}}{{end}}{{"\t"}}{{range .status.conditions}}{{if eq .type "Degraded"}}{{.status}}{{end}}{{end}}{{"\n"}}{{end}}')
  while IFS=$'\t' read -r name available progressing degraded; do
    [[ -z $name ]] && continue
    if [[ $available != "True" || $progressing != "False" || $degraded != "False" ]]; then
      die "CRC ClusterOperator/$name is not healthy: Available=$available Progressing=$progressing Degraded=$degraded"
    fi
  done <<<"$operator_conditions"

  log VERIFY "CRC target is https://api.crc.testing:6443, current user is kubeadmin, and all ClusterOperators are healthy"
}

assert_operator_test_target_clean() {
  local output
  local resource
  local status
  local -a cluster_resources=(
    "crd/oidcissuers.workloadidentity.azure.micosolutions.se"
    "crd/workloadidentities.workloadidentity.azure.micosolutions.se"
    "crd/workloadidentityrecoveries.workloadidentity.azure.micosolutions.se"
    "validatingwebhookconfiguration/azure-workload-identity-operator-validating-webhook-configuration"
    "mutatingwebhookconfiguration/azure-wi-webhook-mutating-webhook-configuration"
    "clusterrole/azure-workload-identity-operator-manager-role"
    "clusterrole/azure-workload-identity-operator-metrics-auth-role"
    "clusterrole/azure-workload-identity-operator-metrics-reader"
    "clusterrole/azure-wi-webhook-manager-role"
    "clusterrolebinding/azure-workload-identity-operator-manager-rolebinding"
    "clusterrolebinding/azure-workload-identity-operator-metrics-auth-rolebinding"
    "clusterrolebinding/azure-wi-webhook-manager-rolebinding"
  )

  if output=$(helm status "$operator_release" -n "$operator_namespace" 2>&1); then
    die "operator Helm release $operator_namespace/$operator_release already exists; refusing to modify or uninstall it"
  else
    status=$?
  fi
  if [[ $output != *"release: not found"* && $output != *"Release not loaded"* ]]; then
    die "could not prove operator Helm release absence (status $status): $output"
  fi

  for resource in "${cluster_resources[@]}"; do
    if output=$(oc get "$resource" 2>&1); then
      die "$resource already exists; use a fresh CRC cluster"
    elif [[ $output != *"(NotFound)"* ]]; then
      die "could not prove $resource absence: $output"
    fi
  done

  for resource in "$operator_namespace" "$webhook_namespace" "$NAMESPACE"; do
    if output=$(oc get namespace "$resource" 2>&1); then
      die "Namespace/$resource already exists; use a fresh CRC cluster"
    elif [[ $output != *"(NotFound)"* ]]; then
      die "could not prove Namespace/$resource absence: $output"
    fi
  done
}

need oc
need az
need helm
need make
need openssl
need go
need git

provided_operator_credentials=0
for operator_credential in AZURE_CLIENT_ID AZURE_TENANT_ID AZURE_CLIENT_SECRET; do
  [[ -n ${!operator_credential:-} ]] && provided_operator_credentials=$((provided_operator_credentials + 1))
done
case "$provided_operator_credentials" in
  0) use_ephemeral_operator_credentials=true ;;
  3) use_ephemeral_operator_credentials=false ;;
  *) die "AZURE_CLIENT_ID, AZURE_TENANT_ID, and AZURE_CLIENT_SECRET must be set together" ;;
esac

if [[ $use_ephemeral_operator_credentials == "true" && -n ${OPERATOR_AZURE_PRINCIPAL_ID:-} ]]; then
  die "OPERATOR_AZURE_PRINCIPAL_ID cannot be set when the e2e test creates an ephemeral Service Principal"
fi

if [[ -n ${OPERATOR_AZURE_PRINCIPAL_TYPE:-} ]]; then
  die "OPERATOR_AZURE_PRINCIPAL_TYPE is not supported; the operator identity is always a ServicePrincipal"
fi

if [[ -z ${AZURE_SUBSCRIPTION_ID:-} ]]; then
  AZURE_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
fi
AZURE_TENANT_ID=${AZURE_TENANT_ID:-}
AZURE_LOCATION=${AZURE_LOCATION:-swedencentral}
test_run_id=$(openssl rand -hex 4)
AZURE_RESOURCE_GROUP_NAME=${AZURE_RESOURCE_GROUP_NAME:-rg-azwi-crc-platform-test-$test_run_id}
AZURE_KEY_VAULT_RESOURCE_GROUP_NAME=${AZURE_KEY_VAULT_RESOURCE_GROUP_NAME:-rg-azwi-crc-kv-test-$test_run_id}
export AZURE_SUBSCRIPTION_ID
export AZURE_TENANT_ID
export AZURE_LOCATION
export AZURE_RESOURCE_GROUP_NAME
export AZURE_KEY_VAULT_RESOURCE_GROUP_NAME
install_cert_manager=${INSTALL_CERT_MANAGER:-true}
cert_manager_version=${CERT_MANAGER_VERSION:-v1.21.1}
cert_manager_namespace=${CERT_MANAGER_NAMESPACE:-cert-manager}
cert_manager_release=${CERT_MANAGER_RELEASE:-cert-manager}
operator_namespace=${OPERATOR_NAMESPACE:-azure-workload-identity-operator-system}
webhook_namespace=microsoft-azure-workload-identity-webhook-system
operator_release=${OPERATOR_RELEASE:-azure-workload-identity-operator}
operator_image_name=${OPERATOR_IMAGE_NAME:-azure-workload-identity-operator}
operator_candidate_run_id=${OPERATOR_CANDIDATE_RUN_ID:-}
operator_image_repository=${OPERATOR_IMAGE_REPOSITORY:-}
operator_image_digest=${OPERATOR_IMAGE_DIGEST:-}
operator_candidate_commit=${OPERATOR_CANDIDATE_COMMIT:-}
operator_credentials_secret=${OPERATOR_CREDENTIALS_SECRET:-azure-workload-identity-operator-azure-credentials}
operator_oidc_issuer_refresh_interval=${OPERATOR_OIDC_ISSUER_REFRESH_INTERVAL:-1m}
operator_workload_identity_refresh_interval=${OPERATOR_WORKLOAD_IDENTITY_REFRESH_INTERVAL:-1m}
ensure_key_vault=${ENSURE_KEY_VAULT:-true}
enable_key_vault_rbac=${ENABLE_KEY_VAULT_RBAC:-true}
AZURE_STORAGE_ACCOUNT_NAME=${AZURE_STORAGE_ACCOUNT_NAME:-stazwicrc$test_run_id}
AZURE_BLOB_CONTAINER_NAME=${AZURE_BLOB_CONTAINER_NAME:-oidc}
export AZURE_STORAGE_ACCOUNT_NAME
export AZURE_BLOB_CONTAINER_NAME
assign_oidc_storage_blob_role=${ASSIGN_OIDC_STORAGE_BLOB_ROLE:-true}
oidc_storage_blob_role=${OIDC_STORAGE_BLOB_ROLE:-Storage Blob Data Contributor}
AZURE_USER_ASSIGNED_IDENTITY_NAME=${AZURE_USER_ASSIGNED_IDENTITY_NAME:-id-azwi-crc-test}
AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME=${AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME:-fidc-azwi-crc-test}
if [[ -z ${KEY_VAULT_NAME:-} ]]; then
  KEY_VAULT_NAME="kv-azwi-$test_run_id"
fi
KEY_VAULT_SECRET_NAME=${KEY_VAULT_SECRET_NAME:-test-secret}
export AZURE_USER_ASSIGNED_IDENTITY_NAME
export AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME
export KEY_VAULT_NAME
export KEY_VAULT_SECRET_NAME
key_vault_secret_value=${KEY_VAULT_SECRET_VALUE:-"azwi-crc-test smoke secret $(date -u +%Y-%m-%dT%H:%M:%SZ)"}
upload_key_vault_secret=${UPLOAD_KEYVAULT_SECRET:-true}
assign_key_vault_secret_writer_role=${ASSIGN_KEYVAULT_SECRET_WRITER_ROLE:-true}
key_vault_secret_writer_role=${KEY_VAULT_SECRET_WRITER_ROLE:-Key Vault Secrets Officer}
if [[ $ensure_key_vault != "true" && $upload_key_vault_secret == "true" ]]; then
  die "UPLOAD_KEYVAULT_SECRET must be false when ENSURE_KEY_VAULT=false; refusing to replace a secret in an externally owned Key Vault"
fi
NAMESPACE=${NAMESPACE:-azwi-crc-test}
WORKLOAD_IDENTITY_NAME=${WORKLOAD_IDENTITY_NAME:-azwi-crc-test}
SERVICE_ACCOUNT_NAME=${SERVICE_ACCOUNT_NAME:-$WORKLOAD_IDENTITY_NAME}
AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME="$NAMESPACE-$AZURE_USER_ASSIGNED_IDENTITY_NAME"
SIGNING_KEY_SECRET_NAMESPACE=${SIGNING_KEY_SECRET_NAMESPACE:-openshift-kube-apiserver}
SIGNING_KEY_SECRET_NAME=${SIGNING_KEY_SECRET_NAME:-bound-service-account-signing-key}
SIGNING_KEY_SECRET_KEY=${SIGNING_KEY_SECRET_KEY:-service-account.pub}
verify_signing_key_rotation=${VERIFY_SIGNING_KEY_ROTATION:-true}
verify_oidc_issuer_periodic_refresh=${VERIFY_OIDC_ISSUER_PERIODIC_REFRESH:-$verify_signing_key_rotation}
RETIRING_SIGNING_KEY_SECRET_NAMESPACE=${RETIRING_SIGNING_KEY_SECRET_NAMESPACE:-$NAMESPACE}
RETIRING_SIGNING_KEY_SECRET_NAME=${RETIRING_SIGNING_KEY_SECRET_NAME:-azwi-crc-retiring-signing-key}
RETIRING_SIGNING_KEY_SECRET_KEY=${RETIRING_SIGNING_KEY_SECRET_KEY:-service-account.pub}
OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER=${OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER:-true}
OIDC_ISSUER_DELETION_POLICY=${OIDC_ISSUER_DELETION_POLICY:-Delete}
WORKLOAD_IDENTITY_DELETION_POLICY=${WORKLOAD_IDENTITY_DELETION_POLICY:-Delete}
verify_workload_identity_service_account_recreation_enabled=${VERIFY_WORKLOAD_IDENTITY_SERVICE_ACCOUNT_RECREATION:-true}
verify_workload_identity_conflicts=${VERIFY_WORKLOAD_IDENTITY_CONFLICTS:-true}
verify_workload_identity_azure_drift=${VERIFY_WORKLOAD_IDENTITY_AZURE_DRIFT:-true}
verify_workload_identity_controlled_recovery=${VERIFY_WORKLOAD_IDENTITY_CONTROLLED_RECOVERY:-true}
IMAGE_NAME=${IMAGE_NAME:-azwi-crc-test}
JOB_NAME=${JOB_NAME:-azwi-crc-test}
KEY_VAULT_READ_TIMEOUT_SECONDS=${KEY_VAULT_READ_TIMEOUT_SECONDS:-300}
export NAMESPACE
export WORKLOAD_IDENTITY_NAME
export SERVICE_ACCOUNT_NAME
export AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME
export SIGNING_KEY_SECRET_NAMESPACE
export SIGNING_KEY_SECRET_NAME
export SIGNING_KEY_SECRET_KEY
export RETIRING_SIGNING_KEY_SECRET_NAMESPACE
export RETIRING_SIGNING_KEY_SECRET_NAME
export RETIRING_SIGNING_KEY_SECRET_KEY
export OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER
export OIDC_ISSUER_DELETION_POLICY
export WORKLOAD_IDENTITY_DELETION_POLICY
export IMAGE_NAME
export JOB_NAME
export KEY_VAULT_READ_TIMEOUT_SECONDS

assert_crc_cluster_target
assert_operator_test_target_clean

az account set --subscription "$AZURE_SUBSCRIPTION_ID"
active_subscription_id=$(az account show --query id -o tsv)
if [[ $active_subscription_id != "$AZURE_SUBSCRIPTION_ID" ]]; then
  die "active Azure subscription is $active_subscription_id, expected $AZURE_SUBSCRIPTION_ID"
fi
log VERIFY "Azure CLI is using subscription $AZURE_SUBSCRIPTION_ID"

wait_timeout=${WAIT_TIMEOUT:-10m}
azure_rbac_propagation_timeout=${AZURE_RBAC_PROPAGATION_TIMEOUT:-5m}
key_vault_purge_timeout=${KEY_VAULT_PURGE_TIMEOUT:-20m}
assign_role=${ASSIGN_KEYVAULT_ROLE:-true}
key_vault_role=${KEY_VAULT_ROLE:-Key Vault Secrets User}

if [[ $verify_signing_key_rotation == "true" ]]; then
  need curl
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../../.." && pwd)
operator_chart="$repo_root/dist/chart"
tmpdir=""
operator_credentials_dir=""
operator_paused=false
vault_id=""
# Cleanup ownership:
# - script-created resources are tracked here and deleted by this script
# - operator-created child resources are deleted only by deleting their CRs and waiting for finalizers
# - the shared group starts absent and is operator-created, but the script assumes explicit cleanup responsibility
applied_oidc_issuer=false
applied_workload_identity=false
created_buildconfig=false
applied_job=false
applied_conflict_workload_identity=false
applied_federated_credential_conflict_workload_identity=false
applied_workload_identity_recovery=false
created_recovery_blocker_fic=false
controlled_recovery_started=false
controlled_recovery_completed=false
created_conflict_service_account=false
oidc_deleted=false
workload_identity_deleted=false
expect_workload_identity_service_account_deleted=false
original_openshift_service_account_issuer=""
original_openshift_service_account_token_issuer=""
# The original issuer can be an empty string, so track whether capture actually ran.
captured_original_openshift_service_account_issuer=false
created_operator_namespace=false
created_webhook_namespace=false
created_operator_release=false
created_operator_buildconfig=false
created_operator_credentials_secret=false
created_operator_crds=false
created_cert_manager_namespace=false
created_cert_manager_release=false
created_cert_manager_crds=false
created_test_namespace=false
created_retiring_signing_key_secret=false
created_ephemeral_operator_application=false
ephemeral_operator_application_id=""
ephemeral_operator_application_name=""
operator_principal_id=${OPERATOR_AZURE_PRINCIPAL_ID:-}
operator_azure_resource_role=${OPERATOR_AZURE_RESOURCE_ROLE:-Contributor}
active_azure_principal_id=${AZURE_CLI_PRINCIPAL_ID:-}
active_azure_principal_type=${AZURE_CLI_PRINCIPAL_TYPE:-}
primary_failure=""
cleanup_failed=false
created_key_vault_resource_group=false
created_key_vault=false
cleanup_shared_resource_group_enabled=false
created_role_assignment_ids=()

cleanup_job() {
  if [[ $applied_job == "true" ]]; then
    cleanup_kubernetes_resource "job/$JOB_NAME" "$NAMESPACE" || return $?
  fi
}

cleanup_conflict_workload_identity() {
  if [[ $applied_conflict_workload_identity == "true" ]]; then
    cleanup_kubernetes_resource workloadidentity/azwi-sa-conflict "$NAMESPACE" || return $?
    applied_conflict_workload_identity=false
  fi
  if [[ $applied_federated_credential_conflict_workload_identity == "true" ]]; then
    cleanup_kubernetes_resource workloadidentity/azwi-fic-conflict "$NAMESPACE" || return $?
    applied_federated_credential_conflict_workload_identity=false
  fi
}

cleanup_conflict_service_account() {
  if [[ $created_conflict_service_account == "true" ]]; then
    cleanup_kubernetes_resource serviceaccount/azwi-sa-conflict "$NAMESPACE" || return $?
    created_conflict_service_account=false
  fi
}

cleanup_workload_identity_recovery() {
  if [[ $applied_workload_identity_recovery == "true" ]]; then
    cleanup_kubernetes_resource workloadidentityrecovery/"$WORKLOAD_IDENTITY_NAME-recovery" "" || return $?
    applied_workload_identity_recovery=false
  fi
  if [[ $created_recovery_blocker_fic == "true" ]]; then
    az identity federated-credential delete \
      --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
      --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
      --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME-recovery-blocker" \
      --yes >/dev/null 2>&1 || true
    created_recovery_blocker_fic=false
  fi
}

cleanup_workload_identity() {
  workload_identity_deleted=false
  if [[ $applied_workload_identity == "true" ]]; then
    if cleanup_kubernetes_resource "workloadidentity/$WORKLOAD_IDENTITY_NAME" "$NAMESPACE"; then
      workload_identity_deleted=true
      if [[ $expect_workload_identity_service_account_deleted == "true" &&
        $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" &&
        ( $controlled_recovery_started != "true" || $controlled_recovery_completed == "true" ) ]]; then
        log WATCH "Waiting for WorkloadIdentity deletion to remove operator-created ServiceAccount/$SERVICE_ACCOUNT_NAME"
        wait_for_kubernetes_resource_absent "serviceaccount/$SERVICE_ACCOUNT_NAME" "$NAMESPACE" || return $?
        log VERIFY "WorkloadIdentity deletion removed operator-created ServiceAccount/$SERVICE_ACCOUNT_NAME"
      fi
      if [[ $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" &&
        ( $controlled_recovery_started != "true" || $controlled_recovery_completed == "true" ) ]]; then
        wait_for_workload_identity_azure_resources_deleted || return $?
      elif [[ $controlled_recovery_started == "true" && $controlled_recovery_completed != "true" ]]; then
        log SKIP "Skipping per-resource Azure deletion wait because controlled recovery did not commit"
      fi
    else
      return 1
    fi
  fi
}

cleanup_oidc_issuer() {
  local verify_openshift_handoff_guard=${1:-false}
  oidc_deleted=false
  if [[ $applied_oidc_issuer == "true" ]]; then
    if [[ $verify_openshift_handoff_guard == "true" ]]; then
      begin_step 22
      assert_oidcissuer_delete_rejected_by_openshift_service_account_issuer || return 1
    fi
    begin_step 23
    handoff_openshift_service_account_issuer_before_oidcissuer_delete || return 1
    if cleanup_kubernetes_resource oidcissuer/default ""; then
      oidc_deleted=true
      if [[ $OIDC_ISSUER_DELETION_POLICY == "Delete" ]]; then
        wait_for_oidc_storage_account_deleted || return $?
      fi
    else
      return 1
    fi
  fi
}

verify_shared_resource_group_retained() {
  local resource_group_status

  if [[ $workload_identity_deleted == "true" && $oidc_deleted == "true" ]]; then
    if azure_resource_group_exists "$AZURE_RESOURCE_GROUP_NAME"; then
      log VERIFY "Operator retained shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
    else
      resource_group_status=$?
      if [[ $resource_group_status -eq 1 ]]; then
        log ERROR "Shared platform resource group $AZURE_RESOURCE_GROUP_NAME was deleted by the operator"
      else
        log ERROR "Could not verify shared platform resource group retention: $azure_resource_group_check_error"
      fi
      return 1
    fi
  fi
}

cleanup_build_artifacts() {
  if [[ $created_buildconfig == "true" ]]; then
    if ! kubernetes_cleanup_command "$wait_timeout" oc delete buildconfig "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null; then
      kubernetes_cleanup_failure "delete OpenShift BuildConfig/$IMAGE_NAME"
      return $?
    fi
    if ! kubernetes_cleanup_command "$wait_timeout" oc delete imagestream "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null; then
      kubernetes_cleanup_failure "delete OpenShift ImageStream/$IMAGE_NAME"
      return $?
    fi
    wait_for_kubernetes_resource_absent "buildconfig/$IMAGE_NAME" "$NAMESPACE" || return $?
    wait_for_kubernetes_resource_absent "imagestream/$IMAGE_NAME" "$NAMESPACE" || return $?
  fi
}

pause_operator() {
  log PAUSE "Scaling the packaged operator to zero replicas"
  if ! oc scale deployment azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" --replicas=0 >/dev/null; then
    log ERROR "Could not scale the packaged operator to zero replicas"
    return 1
  fi
  if ! oc wait pod -n "$operator_namespace" \
    -l control-plane=controller-manager \
    --for=delete --timeout="$wait_timeout" >/dev/null 2>&1; then
    if oc get pod -n "$operator_namespace" -l control-plane=controller-manager -o name | grep -q .; then
      log ERROR "Timed out waiting for packaged operator Pods to stop"
      return 1
    fi
  fi
  operator_paused=true
}

resume_operator() {
  if [[ $operator_paused != "true" ]]; then
    return
  fi
  log RUN "Scaling the packaged operator back to two replicas"
  if ! oc scale deployment azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" --replicas=2 >/dev/null; then
    log ERROR "Could not scale the packaged operator back to two replicas"
    return 1
  fi
  if ! oc rollout status deployment/azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" --timeout="$wait_timeout"; then
    log ERROR "Packaged operator did not become Ready after resuming"
    return 1
  fi
  operator_paused=false
}

cleanup_operator_credentials_secret() {
  if [[ $created_operator_credentials_secret == "true" ]]; then
    cleanup_kubernetes_resource "secret/$operator_credentials_secret" "$operator_namespace" || return $?
    created_operator_credentials_secret=false
  fi
}

cleanup_ephemeral_operator_identity() {
  local application_id
  local delete_output
  local deadline
  local service_principal_id

  if [[ $created_ephemeral_operator_application != "true" || -z $ephemeral_operator_application_id ]]; then
    return 0
  fi

  log DELETE "Deleting ephemeral operator Entra application $ephemeral_operator_application_name ($ephemeral_operator_application_id)"
  deadline=$((SECONDS + $(duration_seconds "$azure_rbac_propagation_timeout")))
  while (( SECONDS < deadline )); do
    if ! application_id=$(az ad app list \
      --filter "appId eq '$ephemeral_operator_application_id'" \
      --query '[0].appId' \
      --only-show-errors \
      -o tsv 2>&1); then
      log RETRY "Could not verify ephemeral Entra application deletion: $application_id"
      sleep_until_deadline "$deadline" 5 || true
      continue
    fi

    if ! service_principal_id=$(az ad sp list \
      --filter "appId eq '$ephemeral_operator_application_id'" \
      --query '[0].id' \
      --only-show-errors \
      -o tsv 2>&1); then
      log RETRY "Could not verify ephemeral Service Principal deletion: $service_principal_id"
      sleep_until_deadline "$deadline" 5 || true
      continue
    fi

    if [[ -z $application_id && -z $service_principal_id ]]; then
      created_ephemeral_operator_application=false
      log VERIFY "Deleted ephemeral operator Entra application and Service Principal"
      return 0
    fi

    if [[ -n $application_id ]]; then
      if ! delete_output=$(az ad app delete \
        --id "$ephemeral_operator_application_id" \
        --only-show-errors 2>&1); then
        log RETRY "Could not delete ephemeral Entra application yet: $delete_output"
      fi
    elif [[ -n $service_principal_id ]]; then
      if ! delete_output=$(az ad sp delete \
        --id "$service_principal_id" \
        --only-show-errors 2>&1); then
        log RETRY "Could not delete orphaned ephemeral Service Principal yet: $delete_output"
      fi
    fi
    sleep_until_deadline "$deadline" 5 || true
  done

  log ERROR "Ephemeral operator identity still exists; delete application $ephemeral_operator_application_id manually"
  return 1
}

cleanup_operator_release() {
  if [[ $operator_paused == "true" ]]; then
    resume_operator >/dev/null 2>&1 || true
  fi
  if [[ $created_operator_release == "true" ]]; then
    cleanup_helm_release "$operator_release" "$operator_namespace" || return $?
    created_operator_release=false
  fi
}

cleanup_operator_crds() {
  if [[ $created_operator_crds == "true" ]]; then
    log DELETE "Deleting operator CRDs retained by Helm"
    kubernetes_cleanup_command "$wait_timeout" oc delete crd \
      oidcissuers.workloadidentity.azure.micosolutions.se \
      workloadidentities.workloadidentity.azure.micosolutions.se \
      workloadidentityrecoveries.workloadidentity.azure.micosolutions.se \
      --ignore-not-found --wait=true --timeout="$wait_timeout" >/dev/null || return $?
    created_operator_crds=false
  fi
}

cleanup_operator_build_artifacts() {
  if [[ $created_operator_buildconfig == "true" ]]; then
    kubernetes_cleanup_command "$wait_timeout" oc delete buildconfig "$operator_image_name" \
      -n "$operator_namespace" --ignore-not-found --wait=false >/dev/null || return $?
    kubernetes_cleanup_command "$wait_timeout" oc delete imagestream "$operator_image_name" \
      -n "$operator_namespace" --ignore-not-found --wait=false >/dev/null || return $?
    created_operator_buildconfig=false
  fi
}

cleanup_operator_namespace() {
  if [[ $created_operator_namespace == "true" ]]; then
    cleanup_namespace "$operator_namespace" || return $?
    created_operator_namespace=false
  fi
}

cleanup_webhook_namespace() {
  if [[ $created_webhook_namespace == "true" ]]; then
    cleanup_namespace "$webhook_namespace" || return $?
    created_webhook_namespace=false
  fi
}

cleanup_cert_manager_release() {
  if [[ $created_cert_manager_release == "true" ]]; then
    cleanup_helm_release "$cert_manager_release" "$cert_manager_namespace" || return $?
    created_cert_manager_release=false
  fi
}

cleanup_cert_manager_crds() {
  if [[ $created_cert_manager_crds == "true" ]]; then
    log DELETE "Deleting cert-manager CRDs created by the e2e test"
    kubernetes_cleanup_command "$wait_timeout" oc delete crd \
      certificaterequests.cert-manager.io certificates.cert-manager.io challenges.acme.cert-manager.io \
      clusterissuers.cert-manager.io issuers.cert-manager.io orders.acme.cert-manager.io \
      --ignore-not-found --wait=true --timeout="$wait_timeout" >/dev/null || return $?
    created_cert_manager_crds=false
  fi
}

cleanup_cert_manager_namespace() {
  if [[ $created_cert_manager_namespace == "true" ]]; then
    cleanup_namespace "$cert_manager_namespace" || return $?
    created_cert_manager_namespace=false
  fi
}

cleanup_test_namespace() {
  if [[ $created_test_namespace == "true" ]]; then
    log DELETE "Deleting test namespace $NAMESPACE created by e2e test"
    cleanup_namespace "$NAMESPACE" || return $?
  fi
}

cleanup_retiring_signing_key_secret() {
  if [[ $created_retiring_signing_key_secret == "true" ]]; then
    if ! kubernetes_cleanup_command "$wait_timeout" oc delete secret "$RETIRING_SIGNING_KEY_SECRET_NAME" -n "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" --ignore-not-found >/dev/null; then
      kubernetes_cleanup_failure "delete retiring signing key Secret"
      return $?
    fi
  fi
}

cleanup_tmpdir() {
  if [[ -z $tmpdir ]]; then
    return 0
  fi
  rm -rf "$tmpdir"
}

cleanup_key_vault_resource_group() {
  if [[ $created_key_vault == "true" ]]; then
    log DELETE "Deleting Key Vault $KEY_VAULT_NAME"
    az keyvault delete -n "$KEY_VAULT_NAME" -g "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" -o none >/dev/null || return 1
    purge_deleted_key_vault false || return 1
    created_key_vault=false
  fi

  if [[ $created_key_vault_resource_group == "true" ]]; then
    log DELETE "Deleting Key Vault resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
    az group delete -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" --yes --no-wait >/dev/null || return 1
    wait_for_azure_resource_group_deleted "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" "$wait_timeout" "Script deleted Key Vault Azure resource group"
  fi

  return 0
}

cleanup_shared_resource_group() {
  local resource_group_status

  if [[ $cleanup_shared_resource_group_enabled != "true" ]]; then
    return
  fi
  if azure_resource_group_exists "$AZURE_RESOURCE_GROUP_NAME"; then
    :
  else
    resource_group_status=$?
    if [[ $resource_group_status -eq 1 ]]; then
      return
    fi
    log ERROR "Could not check Azure resource group $AZURE_RESOURCE_GROUP_NAME before cleanup: $azure_resource_group_check_error"
    return 1
  fi

  log DELETE "Deleting operator-created shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
  az group delete -n "$AZURE_RESOURCE_GROUP_NAME" --yes --no-wait >/dev/null || return 1
  wait_for_azure_resource_group_deleted "$AZURE_RESOURCE_GROUP_NAME" "$wait_timeout" "Script deleted shared platform Azure resource group"
}

cleanup_role_assignments() {
  local assignment_id
  for assignment_id in "${created_role_assignment_ids[@]}"; do
    log DELETE "Deleting role assignment $assignment_id"
    az role assignment delete --ids "$assignment_id" >/dev/null 2>&1 || true
    wait_for_role_assignment_deleted "$assignment_id" || return 1
  done
}

wait_for_kubernetes_resource_absent() {
  local resource=$1
  local namespace=${2:-}
  local timeout=${3:-$wait_timeout}
  local deadline
  local exists_status
  local -a get_command
  deadline=$((SECONDS + $(duration_seconds "$timeout")))
  get_command=(oc get "$resource")
  if [[ -n $namespace ]]; then
    get_command+=(-n "$namespace")
  fi

  while (( SECONDS < deadline )); do
    if kubernetes_resource_exists_for_cleanup "${get_command[@]}" >/dev/null; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0) ;;
      1) return 0 ;;
      2)
        kubernetes_cleanup_failure "check Kubernetes resource absence: $namespace/$resource"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying cleanup discovery"
        ;;
    esac
    sleep_until_deadline "$deadline" 2 || true
  done

  log ERROR "Kubernetes resource still exists: $namespace/$resource"
  kubernetes_cleanup_failure "wait for Kubernetes resource to be absent: $namespace/$resource"
  return $?
}

cleanup_kubernetes_resource() {
  local resource=$1
  local namespace=${2:-}
  local -a delete_command
  delete_command=(oc delete "$resource" --ignore-not-found --wait=false)
  if [[ -n $namespace ]]; then
    delete_command+=(-n "$namespace")
  fi

  if ! kubernetes_cleanup_command "$wait_timeout" "${delete_command[@]}" >/dev/null; then
    kubernetes_cleanup_failure "delete Kubernetes resource ${namespace:+$namespace/}$resource"
    return $?
  fi

  wait_for_kubernetes_resource_absent "$resource" "$namespace" || return $?
}

kubernetes_cleanup_command() {
  local timeout=$1
  shift
  local deadline
  local output
  local status
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while true; do
    if output=$("$@" 2>&1); then
      [[ -n $output ]] && printf '%s\n' "$output"
      return 0
    else
      status=$?
    fi

    if ! is_transient_kubernetes_error "$output"; then
      [[ -n $output ]] && printf '%s\n' "$output" >&2
      return "$status"
    fi

    if (( SECONDS >= deadline )); then
      [[ -n $output ]] && log ERROR "$output"
      return "$status"
    fi

    log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying cleanup command"
    sleep_until_deadline "$deadline" 10 || true
  done
}

cleanup_helm_release() {
  local release=$1
  local namespace=$2
  local deadline
  local exists_status
  local remaining
  local attempt_timeout
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while (( SECONDS < deadline )); do
    if helm_release_exists_for_cleanup "$release" "$namespace"; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0) break ;;
      1) return 0 ;;
      2)
        kubernetes_cleanup_failure "check Helm release $namespace/$release"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying Helm release check"
        sleep_until_deadline "$deadline" 10 || true
        ;;
    esac
  done

  if (( SECONDS >= deadline )); then
    kubernetes_cleanup_failure "check Helm release $namespace/$release"
    return $?
  fi

  log DELETE "Uninstalling Helm release $namespace/$release"
  while (( SECONDS < deadline )); do
    remaining=$((deadline - SECONDS))
    attempt_timeout=$remaining
    if (( attempt_timeout > 60 )); then
      attempt_timeout=60
    fi

    if helm_uninstall_for_cleanup "$release" "$namespace" "${attempt_timeout}s"; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0) break ;;
      2)
        kubernetes_cleanup_failure "uninstall Helm release $namespace/$release"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying Helm uninstall"
        ;;
    esac
    sleep_until_deadline "$deadline" 10 || true
  done

  if (( SECONDS >= deadline )); then
    kubernetes_cleanup_failure "uninstall Helm release $namespace/$release"
    return $?
  fi

  while (( SECONDS < deadline )); do
    if helm_release_exists_for_cleanup "$release" "$namespace"; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0)
        log RETRY "Helm release $namespace/$release still exists after uninstall; waiting"
        ;;
      1) return 0 ;;
      2)
        kubernetes_cleanup_failure "verify Helm release $namespace/$release removal"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying Helm release removal check"
        ;;
    esac
    sleep_until_deadline "$deadline" 10 || true
  done

  kubernetes_cleanup_failure "verify Helm release $namespace/$release removal"
  return $?
}

helm_release_exists_for_cleanup() {
  local release=$1
  local namespace=$2
  local output
  local status

  if output=$(helm status "$release" -n "$namespace" 2>&1); then
    return 0
  else
    status=$?
  fi

  if is_helm_release_not_found_error "$output"; then
    return 1
  fi

  if is_transient_kubernetes_error "$output"; then
    return 3
  fi

  [[ -n $output ]] && printf '%s\n' "$output" >&2
  return 2
}

helm_uninstall_for_cleanup() {
  local release=$1
  local namespace=$2
  local timeout=$3
  local output
  local status

  if output=$(helm uninstall "$release" -n "$namespace" --wait --timeout "$timeout" 2>&1); then
    [[ -n $output ]] && printf '%s\n' "$output"
    return 0
  else
    status=$?
  fi

  if is_helm_release_not_found_error "$output"; then
    return 0
  fi

  if is_transient_kubernetes_error "$output"; then
    return 3
  fi

  if is_helm_wait_timeout_error "$output"; then
    return 3
  fi

  [[ -n $output ]] && printf '%s\n' "$output" >&2
  return 2
}

helm_release_status_for_cleanup() {
  local release=$1
  local namespace=$2
  local output
  local status
  local release_status

  if output=$(helm status "$release" -n "$namespace" -o json 2>&1); then
    release_status=$(printf '%s\n' "$output" | sed -n 's/.*"status":[[:space:]]*"\([^"]*\)".*/\1/p')
    printf '%s\n' "$release_status"
    return 0
  else
    status=$?
  fi

  if is_helm_release_not_found_error "$output"; then
    return 1
  fi

  if is_transient_kubernetes_error "$output"; then
    return 3
  fi

  [[ -n $output ]] && printf '%s\n' "$output" >&2
  return 2
}

is_helm_release_not_found_error() {
  local output=$1
  [[ $output == *"release: not found"* ||
    $output == *"Release not loaded"* ]]
}

is_helm_wait_timeout_error() {
  local output=$1
  [[ $output == *"timed out waiting for the condition"* ||
    $output == *"timed out waiting for resources"* ]]
}

kubernetes_resource_exists_for_cleanup() {
  local output
  local status

  if output=$("$@" 2>&1); then
    [[ -n $output ]] && printf '%s\n' "$output"
    return 0
  else
    status=$?
  fi

  if is_kubernetes_not_found_error "$output"; then
    return 1
  fi

  if is_transient_kubernetes_error "$output"; then
    return 3
  fi

  [[ -n $output ]] && printf '%s\n' "$output" >&2
  return 2
}

is_transient_kubernetes_error() {
  local output=$1
  [[ $output == *"connection reset by peer"* ||
    $output == *"connection refused"* ||
    $output == *"Client.Timeout exceeded"* ||
    $output == *"context deadline exceeded"* ||
    $output == *"net/http: TLS handshake timeout"* ||
    $output == *"i/o timeout"* ||
    $output == *"EOF"* ||
    $output == *"Unauthorized"* ||
    $output == *"unexpected response: 400"* ||
    $output == *"the server has asked for the client to provide credentials"* ]]
}

is_kubernetes_not_found_error() {
  local output=$1
  [[ $output == *"(NotFound)"* ]]
}

kubernetes_cleanup_failure() {
  local action=$1
  log ERROR "Kubernetes cleanup failed after retrying expected CRC/OpenShift API and authentication resets: $action"
  return 1
}

sleep_until_deadline() {
  local deadline=$1
  local requested_sleep=$2
  local remaining
  remaining=$((deadline - SECONDS))

  if (( remaining <= 0 )); then
    return 1
  fi
  if (( requested_sleep > remaining )); then
    requested_sleep=$remaining
  fi
  sleep "$requested_sleep"
}

cleanup_namespace() {
  local namespace=$1
  local deadline
  local exists_status
  local phase
  local conditions
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  if ! kubernetes_cleanup_command "$wait_timeout" oc delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; then
    kubernetes_cleanup_failure "delete namespace $namespace"
    return $?
  fi

  while (( SECONDS < deadline )); do
    if kubernetes_resource_exists_for_cleanup oc get namespace "$namespace" >/dev/null; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0) ;;
      1) return 0 ;;
      2)
        kubernetes_cleanup_failure "check namespace $namespace deletion"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying namespace deletion check"
        ;;
    esac
    phase=$(oc get namespace "$namespace" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ $phase == "Terminating" ]]; then
      conditions=$(oc get namespace "$namespace" -o jsonpath='{range .status.conditions[*]}{.type}={.status}:{.reason}{";"}{end}' 2>/dev/null || true)
      if [[ $conditions == *"NamespaceDeletionDiscoveryFailure=True"* ]]; then
        log RETRY "Namespace $namespace is waiting for API discovery during OpenShift API/authentication rollout"
      fi
    fi
    sleep_until_deadline "$deadline" 10 || true
  done

  kubernetes_cleanup_failure "wait for namespace $namespace deletion"
  return $?
}

wait_for_role_assignment_deleted() {
  local assignment_id=$1
  local deadline
  local existing_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while (( SECONDS < deadline )); do
    if ! existing_id=$(az role assignment list \
      --all \
      --query "[?id=='$assignment_id'].id | [0]" \
      --only-show-errors \
      -o tsv 2>&1); then
      log RETRY "Could not verify role assignment deletion for $assignment_id: $existing_id"
      sleep_until_deadline "$deadline" 5 || true
      continue
    fi
    if [[ -z $existing_id ]]; then
      return 0
    fi
    sleep_until_deadline "$deadline" 5 || true
  done

  log ERROR "Could not prove role assignment deletion: $assignment_id"
  return 1
}

purge_deleted_key_vault() {
  local wait_for_purge=${1:-true}
  local deadline
  local deleted_id
  local location
  deadline=$((SECONDS + $(duration_seconds "$key_vault_purge_timeout")))

  if ! deleted_id=$(az keyvault list-deleted \
    --query "[?name=='$KEY_VAULT_NAME'].id | [0]" \
    -o tsv 2>&1); then
    log ERROR "Could not check for soft-deleted Key Vault $KEY_VAULT_NAME: $deleted_id"
    return 1
  fi
  if [[ -z $deleted_id ]]; then
    return 0
  fi

  if ! location=$(az keyvault list-deleted \
    --query "[?name=='$KEY_VAULT_NAME'].properties.location | [0]" \
    -o tsv 2>&1); then
    log ERROR "Could not resolve the location of soft-deleted Key Vault $KEY_VAULT_NAME: $location"
    return 1
  fi
  [[ -n $location ]] || {
    log ERROR "Soft-deleted Key Vault $KEY_VAULT_NAME has no location"
    return 1
  }
  log DELETE "Purging soft-deleted Key Vault $KEY_VAULT_NAME in $location"
  if ! az rest --method post --url "https://management.azure.com/subscriptions/$AZURE_SUBSCRIPTION_ID/providers/Microsoft.KeyVault/locations/$location/deletedVaults/$KEY_VAULT_NAME/purge?api-version=2023-07-01" >/dev/null 2>&1; then
    if [[ $wait_for_purge != "true" ]]; then
      log SKIP "Could not submit Key Vault purge request; Azure will purge $KEY_VAULT_NAME on its scheduled purge date"
      return 0
    fi
    return 1
  fi

  if [[ $wait_for_purge != "true" ]]; then
    log SKIP "Submitted Key Vault purge request for $KEY_VAULT_NAME without waiting for Azure to complete it"
    return 0
  fi

  while (( SECONDS < deadline )); do
    if ! deleted_id=$(az keyvault list-deleted \
      --query "[?name=='$KEY_VAULT_NAME'].id | [0]" \
      -o tsv 2>&1); then
      log RETRY "Could not verify Key Vault purge for $KEY_VAULT_NAME: $deleted_id"
      sleep_until_deadline "$deadline" 10 || true
      continue
    fi
    if [[ -z $deleted_id ]]; then
      return 0
    fi
    sleep_until_deadline "$deadline" 10 || true
  done

  if ! deleted_id=$(az keyvault list-deleted \
    --query "[?name=='$KEY_VAULT_NAME'].id | [0]" \
    -o tsv 2>&1); then
    log ERROR "Could not verify Key Vault purge for $KEY_VAULT_NAME: $deleted_id"
    return 1
  fi
  if [[ -z $deleted_id ]]; then
    return 0
  fi

  log ERROR "Timed out waiting for soft-deleted Key Vault $KEY_VAULT_NAME to be purged"
  return 1
}

cleanup() {
  local exit_code=$?
  local final_result_message
  local final_result_operation
  local initial_exit_code=$exit_code
  local failed_step=${current_script_step:-unknown}
  local verify_openshift_handoff_guard=false
  set +e

  [[ $initial_exit_code -eq 0 ]] && verify_openshift_handoff_guard=true

  if [[ $verify_openshift_handoff_guard == "true" ]]; then
    begin_step 22
  else
    begin_step 23
  fi
  if [[ $initial_exit_code -eq 0 ]]; then
    log CLEANUP "Cleaning up e2e resources"
  else
    log CLEANUP "Cleaning up e2e resources after failure in step $failed_step"
  fi

  cleanup_step "resume packaged operator for finalizer cleanup" resume_operator
  cleanup_step "delete Job" cleanup_job
  cleanup_step "delete conflict WorkloadIdentity CR" cleanup_conflict_workload_identity
  cleanup_step "delete conflict ServiceAccount" cleanup_conflict_service_account
  cleanup_step "delete WorkloadIdentityRecovery CR" cleanup_workload_identity_recovery
  cleanup_step "delete WorkloadIdentity CR" cleanup_workload_identity
  cleanup_step "delete OIDCIssuer CR" cleanup_oidc_issuer "$verify_openshift_handoff_guard"
  cleanup_step "verify shared platform resource group retention" verify_shared_resource_group_retained
  cleanup_step "delete script-created role assignments" cleanup_role_assignments
  cleanup_step "delete OpenShift build artifacts" cleanup_build_artifacts
  cleanup_step "delete Key Vault resources" cleanup_key_vault_resource_group
  cleanup_step "delete retiring signing key Secret" cleanup_retiring_signing_key_secret
  cleanup_step "delete operator-created shared platform resource group" cleanup_shared_resource_group
  cleanup_step "uninstall operator Helm release" cleanup_operator_release
  cleanup_step "delete operator credentials Secret" cleanup_operator_credentials_secret
  cleanup_step "delete operator CRDs retained by Helm" cleanup_operator_crds
  cleanup_step "delete operator OpenShift build artifacts" cleanup_operator_build_artifacts
  cleanup_step "delete test namespace" cleanup_test_namespace
  cleanup_step "delete retained Azure workload identity webhook namespace" cleanup_webhook_namespace
  cleanup_step "delete operator namespace" cleanup_operator_namespace
  cleanup_step "uninstall cert-manager" cleanup_cert_manager_release
  cleanup_step "delete cert-manager CRDs" cleanup_cert_manager_crds
  cleanup_step "delete cert-manager namespace" cleanup_cert_manager_namespace
  cleanup_step "delete ephemeral operator Entra application" cleanup_ephemeral_operator_identity
  cleanup_step "delete temporary directory" cleanup_tmpdir

  if [[ $initial_exit_code -eq 0 && $cleanup_failed != "true" ]]; then
    final_result_operation=PASS
    final_result_message="OpenShift e2e test passed"
  else
    if [[ -n $primary_failure ]]; then
      final_result_message="OpenShift e2e test failed: $primary_failure"
    elif [[ $initial_exit_code -ne 0 && $cleanup_failed == "true" ]]; then
      final_result_message="OpenShift e2e validation failed in step $failed_step, and cleanup had errors"
    elif [[ $initial_exit_code -ne 0 ]]; then
      final_result_message="OpenShift e2e validation failed in step $failed_step; cleanup completed"
    else
      final_result_message="OpenShift e2e validation passed, but cleanup failed"
    fi
    [[ $cleanup_failed == "true" ]] && log ERROR "Cleanup had errors; inspect earlier CLEANUP/ERROR lines"
    final_result_operation=FAIL
    exit_code=1
  fi

  log "$final_result_operation" "$final_result_message"

  exit "$exit_code"
}
trap cleanup EXIT

cleanup_step() {
  local name=$1
  shift

  if ! "$@"; then
    log ERROR "Cleanup step failed: $name"
    cleanup_failed=true
  fi
}

duration_seconds() {
  local value=$1
  local number
  local unit
  if [[ $value =~ ^([0-9]+)([smh]?)$ ]]; then
    number=${BASH_REMATCH[1]}
    unit=${BASH_REMATCH[2]}
  else
    die "unsupported duration: $value"
  fi

  case "$unit" in
    s|"") printf '%s\n' "$number" ;;
    m) printf '%s\n' $((number * 60)) ;;
    h) printf '%s\n' $((number * 60 * 60)) ;;
  esac
}

resolve_active_azure_principal() {
  if [[ -n $active_azure_principal_id || -n $active_azure_principal_type ]]; then
    if [[ -z $active_azure_principal_id || -z $active_azure_principal_type ]]; then
      die "AZURE_CLI_PRINCIPAL_ID and AZURE_CLI_PRINCIPAL_TYPE must be set together"
    fi
    case "$active_azure_principal_type" in
      User|ServicePrincipal) ;;
      *) die "AZURE_CLI_PRINCIPAL_TYPE must be User or ServicePrincipal" ;;
    esac
    return
  fi

  local account_type
  local principal_name
  account_type=$(az account show --query user.type -o tsv)
  principal_name=$(az account show --query user.name -o tsv)

  case "$account_type" in
    user)
      if ! active_azure_principal_id=$(az ad signed-in-user show --query id -o tsv 2>/dev/null); then
        die "Could not infer the active Azure CLI user object ID; set AZURE_CLI_PRINCIPAL_ID and AZURE_CLI_PRINCIPAL_TYPE explicitly"
      fi
      active_azure_principal_type=User
      ;;
    servicePrincipal)
      if ! active_azure_principal_id=$(az ad sp show --id "$principal_name" --query id -o tsv 2>/dev/null); then
        die "Could not infer the active Azure CLI service principal object ID; set AZURE_CLI_PRINCIPAL_ID and AZURE_CLI_PRINCIPAL_TYPE explicitly"
      fi
      active_azure_principal_type=ServicePrincipal
      ;;
    *)
      die "Could not infer Azure principal type '$account_type'; set AZURE_CLI_PRINCIPAL_ID and AZURE_CLI_PRINCIPAL_TYPE explicitly"
      ;;
  esac

  if [[ -z $active_azure_principal_id ]]; then
    die "Could not infer the active Azure CLI principal object ID; set AZURE_CLI_PRINCIPAL_ID and AZURE_CLI_PRINCIPAL_TYPE explicitly"
  fi
}

ensure_role_assignment() {
  local principal_id=$1
  local principal_type=$2
  local role=$3
  local scope=$4
  local created_id
  local create_output
  local deadline
  local existing_id

  existing_id=$(az role assignment list --assignee "$principal_id" --role "$role" --scope "$scope" --query '[0].id' -o tsv)
  if [[ -n $existing_id ]]; then
    log SKIP "Role assignment already exists: role '$role' on '$scope'"
    return
  fi

  log CREATE "Creating role assignment: role '$role' on '$scope'"
  deadline=$((SECONDS + $(duration_seconds "$azure_rbac_propagation_timeout")))
  while (( SECONDS < deadline )); do
    if created_id=$(az role assignment create \
      --assignee-object-id "$principal_id" \
      --assignee-principal-type "$principal_type" \
      --role "$role" \
      --scope "$scope" \
      --query id \
      -o tsv 2>"$tmpdir/role-assignment-error"); then
      if [[ -z $created_id ]]; then
        created_id=$(az role assignment list \
          --assignee "$principal_id" \
          --role "$role" \
          --scope "$scope" \
          --query '[0].id' \
          -o tsv 2>/dev/null || true)
        if [[ -z $created_id ]]; then
          log ERROR "Azure created the role assignment but its ID could not be recovered for cleanup"
          return 1
        fi
      fi
      created_role_assignment_ids+=("$created_id")
      return 0
    fi

    create_output=$(<"$tmpdir/role-assignment-error")
    if [[ $create_output != *"PrincipalNotFound"* && $create_output != *"does not exist in the directory"* ]]; then
      printf '%s\n' "$create_output" >&2
      return 1
    fi
    log RETRY "The new Service Principal is not visible to Azure RBAC yet"
    sleep_until_deadline "$deadline" 5 || true
  done

  printf '%s\n' "$create_output" >&2
  log ERROR "Timed out waiting to create role assignment for Service Principal $principal_id"
  return 1
}

record_ephemeral_operator_application() {
  local application_id
  application_id=$(az ad app list \
    --display-name "$ephemeral_operator_application_name" \
    --query "[?displayName=='$ephemeral_operator_application_name'].appId | [0]" \
    -o tsv 2>/dev/null || true)
  if [[ -n $application_id ]]; then
    ephemeral_operator_application_id=$application_id
    created_ephemeral_operator_application=true
  fi
}

prepare_operator_credentials() {
  local application_output="$tmpdir/ephemeral-operator-application.json"
  local deadline

  operator_credentials_dir="$tmpdir/operator-credentials"
  mkdir -p "$operator_credentials_dir"
  chmod 700 "$operator_credentials_dir"

  if [[ $use_ephemeral_operator_credentials == "true" ]]; then
    ephemeral_operator_application_name="azwi-crc-e2e-$(date -u +%Y%m%d%H%M%S)-$(openssl rand -hex 8)"
    log CREATE "Creating ephemeral operator Entra application $ephemeral_operator_application_name"
    if ! az ad sp create-for-rbac \
      --name "$ephemeral_operator_application_name" \
      --query '{clientId:appId,clientSecret:password,tenantId:tenant}' \
      -o json >"$application_output"; then
      record_ephemeral_operator_application
      die "Could not create the ephemeral operator Service Principal; the Azure CLI identity needs permission to create Entra applications"
    fi

    AZURE_CLIENT_ID=$(jq -r '.clientId // empty' "$application_output")
    AZURE_CLIENT_SECRET=$(jq -r '.clientSecret // empty' "$application_output")
    AZURE_TENANT_ID=$(jq -r '.tenantId // empty' "$application_output")
    ephemeral_operator_application_id=$AZURE_CLIENT_ID
    [[ -n $ephemeral_operator_application_id ]] && created_ephemeral_operator_application=true
    if [[ -z $AZURE_CLIENT_ID || -z $AZURE_CLIENT_SECRET || -z $AZURE_TENANT_ID ]]; then
      if [[ $created_ephemeral_operator_application != "true" ]]; then
        record_ephemeral_operator_application
      fi
      die "Azure CLI returned incomplete credentials for the ephemeral operator Service Principal"
    fi

    deadline=$((SECONDS + $(duration_seconds "$azure_rbac_propagation_timeout")))
    while (( SECONDS < deadline )); do
      operator_principal_id=$(az ad sp show --id "$AZURE_CLIENT_ID" --query id -o tsv 2>/dev/null || true)
      [[ -n $operator_principal_id ]] && break
      sleep_until_deadline "$deadline" 5 || true
    done
    [[ -n $operator_principal_id ]] || die "Timed out waiting for the ephemeral operator Service Principal to become visible"

    ensure_role_assignment \
      "$operator_principal_id" \
      ServicePrincipal \
      "$operator_azure_resource_role" \
      "/subscriptions/$AZURE_SUBSCRIPTION_ID"
    log VERIFY "Created ephemeral operator Service Principal with temporary '$operator_azure_resource_role' access"
  else
    log CONFIG "Using the existing operator Service Principal supplied through the process environment"
    if [[ -z $operator_principal_id ]]; then
      operator_principal_id=$(az ad sp show --id "$AZURE_CLIENT_ID" --query id -o tsv 2>/dev/null || true)
    fi
    [[ -n $operator_principal_id ]] || \
      die "Could not resolve the operator Service Principal object ID; set OPERATOR_AZURE_PRINCIPAL_ID explicitly"
  fi

  printf '%s' "$AZURE_CLIENT_ID" >"$operator_credentials_dir/AZURE_CLIENT_ID"
  printf '%s' "$AZURE_TENANT_ID" >"$operator_credentials_dir/AZURE_TENANT_ID"
  printf '%s' "$AZURE_CLIENT_SECRET" >"$operator_credentials_dir/AZURE_CLIENT_SECRET"
  chmod 600 "$operator_credentials_dir"/*
  rm -f "$application_output"
  unset AZURE_CLIENT_SECRET
  export AZURE_TENANT_ID
}

wait_for_storage_account_id() {
  local deadline
  local storage_account_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log WATCH "Waiting for storage account $AZURE_RESOURCE_GROUP_NAME/$AZURE_STORAGE_ACCOUNT_NAME"
  while (( SECONDS < deadline )); do
    storage_account_id=$(az storage account show \
      -g "$AZURE_RESOURCE_GROUP_NAME" \
      -n "$AZURE_STORAGE_ACCOUNT_NAME" \
      --query id \
      -o tsv 2>/dev/null || true)
    if [[ -n $storage_account_id ]]; then
      printf '%s\n' "$storage_account_id"
      return
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for storage account $AZURE_RESOURCE_GROUP_NAME/$AZURE_STORAGE_ACCOUNT_NAME"
  oc describe oidcissuer default >&2 || true
  exit 1
}

azure_resource_group_check_error=

azure_resource_group_exists() {
  local resource_group=$1
  local output

  azure_resource_group_check_error=
  if ! output=$(az group exists --name "$resource_group" --output tsv 2>&1); then
    azure_resource_group_check_error=${output:-Azure CLI command failed without output}
    return 2
  fi

  case "$output" in
    true|True|TRUE) return 0 ;;
    false|False|FALSE) return 1 ;;
    *)
      azure_resource_group_check_error="Azure CLI returned unexpected resource-group existence result: ${output:-<empty>}"
      return 2
      ;;
  esac
}

wait_for_shared_resource_group_created() {
  local deadline
  local resource_group_status
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log WATCH "Waiting for the operator to create shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
  while (( SECONDS < deadline )); do
    if azure_resource_group_exists "$AZURE_RESOURCE_GROUP_NAME"; then
      log VERIFY "Operator created shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
      return
    else
      resource_group_status=$?
      if [[ $resource_group_status -eq 2 ]]; then
        log RETRY "Could not check Azure resource group $AZURE_RESOURCE_GROUP_NAME: $azure_resource_group_check_error"
      fi
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for the operator to create shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
  return 1
}

assert_shared_resource_group_absent() {
  local resource_group_status

  if azure_resource_group_exists "$AZURE_RESOURCE_GROUP_NAME"; then
    die "Resource group $AZURE_RESOURCE_GROUP_NAME already exists.
This e2e test verifies that the operator creates the configured shared platform group when it is absent.
Delete the resource group or choose another AZURE_RESOURCE_GROUP_NAME."
  else
    resource_group_status=$?
    if [[ $resource_group_status -ne 1 ]]; then
      die "Could not verify that Azure resource group $AZURE_RESOURCE_GROUP_NAME is absent: $azure_resource_group_check_error"
    fi
  fi
  cleanup_shared_resource_group_enabled=true
}

assert_key_vault_resource_group_absent() {
  local resource_group_status

  if [[ $ensure_key_vault != "true" ]]; then
    return
  fi

  if azure_resource_group_exists "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"; then
    die "Resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME already exists.
This e2e test creates and deletes the Key Vault resource group itself.
Delete the resource group or choose another AZURE_KEY_VAULT_RESOURCE_GROUP_NAME."
  else
    resource_group_status=$?
    if [[ $resource_group_status -ne 1 ]]; then
      die "Could not verify that Azure resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME is absent: $azure_resource_group_check_error"
    fi
  fi
}

wait_for_azure_resource_group_deleted() {
  local resource_group=$1
  local timeout=$2
  local success_message=${3:-Azure resource group was deleted}
  local deadline
  local resource_group_status
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while (( SECONDS < deadline )); do
    if azure_resource_group_exists "$resource_group"; then
      :
    else
      resource_group_status=$?
      case "$resource_group_status" in
        1)
          log VERIFY "$success_message: $resource_group"
          return 0
          ;;
        2)
          log RETRY "Could not verify deletion of Azure resource group $resource_group: $azure_resource_group_check_error"
          ;;
      esac
    fi
    sleep 15
  done

  log ERROR "Timed out waiting for Azure resource group $resource_group to be deleted"
  az resource list -g "$resource_group" -o table >&2 || true
  return 1
}

upload_key_vault_secret_with_retry() {
  local deadline
  local output
  deadline=$((SECONDS + $(duration_seconds "$azure_rbac_propagation_timeout")))

  log UPLOAD "Uploading Key Vault secret $KEY_VAULT_SECRET_NAME to $KEY_VAULT_NAME"
  while true; do
    if output=$(az keyvault secret set \
      --vault-name "$KEY_VAULT_NAME" \
      --name "$KEY_VAULT_SECRET_NAME" \
      --value "$key_vault_secret_value" \
      --query id \
      -o tsv 2>&1); then
      log UPLOAD "Uploaded Key Vault secret: $output"
      return
    fi

    if (( SECONDS >= deadline )); then
      log ERROR "Timed out uploading Key Vault secret after waiting for Azure RBAC propagation"
      log ERROR "$output"
      exit 1
    fi

    log RETRY "Key Vault secret upload is not authorized yet; retrying"
    sleep 10
  done
}

wait_for_workload_identity_azure_resources_deleted() {
  local deadline
  local identity_id
  local credential_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log WATCH "Waiting for WorkloadIdentity deletion to remove user assigned identity $AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME and cascade to federated credential $AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME"
  while (( SECONDS < deadline )); do
    identity_id=$(az identity show \
      --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
      --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
      --query id \
      -o tsv 2>/dev/null || true)
    credential_id=$(az identity federated-credential show \
      --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
      --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
      --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
      --query id \
      -o tsv 2>/dev/null || true)
    if [[ -z $identity_id && -z $credential_id ]]; then
      log VERIFY "WorkloadIdentity deletion removed its user assigned identity and Azure removed the child federated credential"
      return
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for WorkloadIdentity Azure resources to be deleted"
  return 1
}

wait_for_oidc_storage_account_deleted() {
  local deadline
  local storage_account_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log WATCH "Waiting for OIDCIssuer deletion to remove storage account $AZURE_STORAGE_ACCOUNT_NAME"
  while (( SECONDS < deadline )); do
    storage_account_id=$(az storage account show \
      --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
      --name "$AZURE_STORAGE_ACCOUNT_NAME" \
      --query id \
      -o tsv 2>/dev/null || true)
    if [[ -z $storage_account_id ]]; then
      log VERIFY "OIDCIssuer deletion removed its operator-created storage account"
      return
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for OIDCIssuer storage account to be deleted"
  return 1
}

install_cert_manager_dependency() {
  local release_preexisting=false

  if oc get crd certificates.cert-manager.io >/dev/null 2>&1; then
    log CONFIG "cert-manager CRDs already exist; the e2e test will not delete them"
  else
    created_cert_manager_crds=true
  fi

  if [[ $install_cert_manager != "true" ]]; then
    oc get crd certificates.cert-manager.io >/dev/null 2>&1 || \
      die "cert-manager is required when INSTALL_CERT_MANAGER=false"
    log SKIP "Using the existing cert-manager installation"
    return
  fi

  if helm status "$cert_manager_release" -n "$cert_manager_namespace" >/dev/null 2>&1; then
    release_preexisting=true
  fi
  if [[ $release_preexisting == "true" ]]; then
    log SKIP "Using existing cert-manager Helm release $cert_manager_namespace/$cert_manager_release"
    return
  fi

  if ! oc get namespace "$cert_manager_namespace" >/dev/null 2>&1; then
    log CREATE "Creating cert-manager namespace $cert_manager_namespace"
    oc create namespace "$cert_manager_namespace" >/dev/null
    created_cert_manager_namespace=true
  fi

  log INSTALL "Installing cert-manager $cert_manager_version"
  helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
  helm repo update jetstack >/dev/null
  created_cert_manager_release=true
  if ! helm upgrade --install "$cert_manager_release" jetstack/cert-manager \
    --version "$cert_manager_version" \
    --namespace "$cert_manager_namespace" \
    --set crds.enabled=true \
    --wait \
    --timeout "$wait_timeout"; then
    log ERROR "Failed to install cert-manager"
    dump_namespaced_resources "$cert_manager_namespace"
    oc get events -n "$cert_manager_namespace" --sort-by=.lastTimestamp >&2 || true
    exit 1
  fi
}

ensure_operator_namespace() {
  if ! oc get namespace "$operator_namespace" >/dev/null 2>&1; then
    log CREATE "Creating operator namespace $operator_namespace"
    oc create namespace "$operator_namespace" >/dev/null
    created_operator_namespace=true
  fi
}

verify_latest_openshift_build_completed() {
  local buildconfig=$1
  local namespace=$2
  local build_name
  local build_phase

  build_name=$(oc get builds \
    -n "$namespace" \
    -l "buildconfig=$buildconfig" \
    --sort-by=.metadata.creationTimestamp \
    -o jsonpath='{.items[-1].metadata.name}' 2>/dev/null || true)
  [[ -n $build_name ]] || die "OpenShift did not create a Build for BuildConfig/$buildconfig"
  build_phase=$(oc get build "$build_name" -n "$namespace" -o jsonpath='{.status.phase}')
  if [[ $build_phase != "Complete" ]]; then
    log ERROR "OpenShift Build/$build_name finished in phase $build_phase"
    oc describe build "$build_name" -n "$namespace" >&2 || true
    return 1
  fi
}

build_operator_image() {
  local operator_build_context="$tmpdir/operator-build-context"

  ensure_operator_namespace
  if oc get buildconfig "$operator_image_name" -n "$operator_namespace" >/dev/null 2>&1; then
    die "OpenShift BuildConfig/$operator_image_name already exists in $operator_namespace; use a fresh CRC cluster"
  fi

  log CREATE "Creating OpenShift BuildConfig/$operator_image_name"
  oc new-build --name="$operator_image_name" --binary --strategy=docker \
    -n "$operator_namespace" >/dev/null
  created_operator_buildconfig=true
  prepare_operator_build_context "$operator_build_context"
  log BUILD "Building the operator image into the internal OpenShift registry"
  if ! oc start-build "$operator_image_name" --from-dir="$operator_build_context" --follow --wait \
    -n "$operator_namespace"; then
    oc get builds,pods -n "$operator_namespace" >&2 || true
    exit 1
  fi

  verify_latest_openshift_build_completed "$operator_image_name" "$operator_namespace" || exit 1
}

prepare_operator_build_context() {
  local build_context=$1
  local source_dir

  mkdir -p "$build_context"
  chmod 700 "$build_context"
  for source_file in Dockerfile go.mod go.sum; do
    cp "$repo_root/$source_file" "$build_context/$source_file"
  done
  for source_dir in api cmd internal; do
    cp -R "$repo_root/$source_dir" "$build_context/$source_dir"
  done

  if find "$build_context" -type f \( \
    -name '.env' -o -name '*.pem' -o -name '*.key' -o -name '*.p12' -o -name '*.pfx' -o \
    -name 'credentials' -o -name 'credentials.json' -o -name '.dockerconfigjson' \
  \) | grep -q .; then
    die "allowlisted operator build context unexpectedly contains a credential-like file"
  fi
  log VERIFY "Prepared an allowlisted operator build context without repository metadata, env files, or credentials"
}

prepare_release_candidate() {
  local actual_commit
  local candidate_chart_file
  local candidate_commit
  local candidate_dir="$tmpdir/release-candidate"
  local candidate_digest
  local candidate_repository
  local candidate_values
  local run_json

  [[ $operator_candidate_run_id =~ ^[0-9]+$ ]] || \
    die "OPERATOR_CANDIDATE_RUN_ID must be a GitHub Actions run ID"
  need gh
  need jq
  need sha256sum

  mkdir -p "$candidate_dir"
  chmod 700 "$candidate_dir"
  log DOWNLOAD "Downloading immutable release-candidate artifact from workflow run $operator_candidate_run_id"
  gh run download "$operator_candidate_run_id" \
    --name release-candidate \
    --dir "$candidate_dir"
  (cd "$candidate_dir" && sha256sum --check SHA256SUMS)

  candidate_commit=$(jq -r .commit "$candidate_dir/candidate.json")
  candidate_repository=$(jq -r .imageRepository "$candidate_dir/candidate.json")
  candidate_digest=$(jq -r .imageDigest "$candidate_dir/candidate.json")
  candidate_chart_file=$(jq -r .chartFile "$candidate_dir/candidate.json")
  [[ $candidate_commit =~ ^[a-f0-9]{40}$ ]] || die "candidate metadata contains an invalid commit"
  [[ $candidate_repository == ghcr.io/* ]] || die "candidate metadata contains an invalid image repository"
  [[ $candidate_digest =~ ^sha256:[a-f0-9]{64}$ ]] || die "candidate metadata contains an invalid image digest"
  [[ $candidate_chart_file =~ ^azure-workload-identity-operator-[0-9]+\.[0-9]+\.[0-9]+\.tgz$ ]] || \
    die "candidate metadata contains an invalid chart filename"
  [[ -f "$candidate_dir/$candidate_chart_file" ]] || die "candidate chart archive is missing"

  run_json=$(gh run view "$operator_candidate_run_id" \
    --json conclusion,event,headSha)
  jq -e --arg sha "$candidate_commit" \
    '.conclusion == "success" and .event == "workflow_dispatch" and .headSha == $sha' \
    <<<"$run_json" >/dev/null || die "workflow run is not a successful workflow_dispatch run for the recorded commit"

  [[ -z $operator_candidate_commit || $operator_candidate_commit == "$candidate_commit" ]] || \
    die "OPERATOR_CANDIDATE_COMMIT does not match candidate metadata"
  [[ -z $operator_image_repository || $operator_image_repository == "$candidate_repository" ]] || \
    die "OPERATOR_IMAGE_REPOSITORY does not match candidate metadata"
  [[ -z $operator_image_digest || $operator_image_digest == "$candidate_digest" ]] || \
    die "OPERATOR_IMAGE_DIGEST does not match candidate metadata"
  operator_candidate_commit=$candidate_commit
  operator_image_repository=$candidate_repository
  operator_image_digest=$candidate_digest
  operator_chart="$candidate_dir/$candidate_chart_file"

  actual_commit=$(git -C "$repo_root" rev-parse HEAD)
  [[ $actual_commit == "$operator_candidate_commit" ]] || \
    die "worktree commit $actual_commit does not match candidate commit $operator_candidate_commit"
  [[ -z $(git -C "$repo_root" status --porcelain) ]] || \
    die "release candidate CRC validation requires a clean worktree"
  candidate_values=$(helm show values "$operator_chart")
  grep -Fq "    repository: $operator_image_repository" <<<"$candidate_values" || \
    die "candidate chart does not embed the candidate image repository"
  grep -Fq "    digest: \"$operator_image_digest\"" <<<"$candidate_values" || \
    die "candidate chart does not embed the candidate image digest"
  log VERIFY "Verified the exact candidate chart, commit, image digest, workflow provenance, and artifact checksums"
}

create_operator_credentials_secret() {
  if oc get secret "$operator_credentials_secret" -n "$operator_namespace" >/dev/null 2>&1; then
    die "Secret/$operator_credentials_secret already exists in $operator_namespace; refusing to overwrite credentials"
  fi

  [[ -f $operator_credentials_dir/AZURE_CLIENT_ID ]] || die "operator client ID file is missing"
  [[ -f $operator_credentials_dir/AZURE_TENANT_ID ]] || die "operator tenant ID file is missing"
  [[ -f $operator_credentials_dir/AZURE_CLIENT_SECRET ]] || die "operator client secret file is missing"

  log CREATE "Creating operator Azure credentials Secret without command-line secret literals"
  oc create secret generic "$operator_credentials_secret" \
    -n "$operator_namespace" \
    --from-file="AZURE_CLIENT_ID=$operator_credentials_dir/AZURE_CLIENT_ID" \
    --from-file="AZURE_TENANT_ID=$operator_credentials_dir/AZURE_TENANT_ID" \
    --from-file="AZURE_CLIENT_SECRET=$operator_credentials_dir/AZURE_CLIENT_SECRET" >/dev/null
  created_operator_credentials_secret=true
}

install_operator_release() {
  local -a image_values

  if [[ -n $operator_candidate_run_id ]]; then
    image_values=(
      --set manager.image.pullPolicy=Always
    )
  else
    operator_image_repository="image-registry.openshift-image-registry.svc:5000/$operator_namespace/$operator_image_name"
    image_values=(
      --set-string "manager.image.repository=$operator_image_repository"
      --set-string manager.image.tag=latest
      --set manager.image.pullPolicy=Always
    )
  fi

  if oc get crd oidcissuers.workloadidentity.azure.micosolutions.se >/dev/null 2>&1; then
    die "operator CRDs appeared after the clean-target check; refusing to adopt them"
  fi
  created_operator_crds=true
  create_operator_credentials_secret

  log INSTALL "Validating and installing the complete operator Helm release"
  if [[ -n $operator_candidate_run_id ]]; then
    helm lint "$operator_chart" \
      --set-string "azure.tenantId=$AZURE_TENANT_ID" \
      --set-string "azure.subscriptionId=$AZURE_SUBSCRIPTION_ID" \
      --set-string "azure.resourceGroupName=$AZURE_RESOURCE_GROUP_NAME" \
      --set-string "azure.location=$AZURE_LOCATION" \
      --set-string "azure.credentials.existingSecret=$operator_credentials_secret"
  else
    make --no-print-directory -C "$repo_root" helm-lint
  fi
  created_operator_release=true
  # The chart intentionally retains this singleton namespace on normal
  # uninstall. A fresh CRC run proves it was absent, so this test owns cleanup.
  created_webhook_namespace=true
  if ! helm upgrade --install "$operator_release" "$operator_chart" \
    --namespace "$operator_namespace" \
    "${image_values[@]}" \
    --set-string "manager.refreshIntervals.oidcIssuer=$operator_oidc_issuer_refresh_interval" \
    --set-string "manager.refreshIntervals.workloadIdentity=$operator_workload_identity_refresh_interval" \
    --set-string "azure.tenantId=$AZURE_TENANT_ID" \
    --set-string "azure.subscriptionId=$AZURE_SUBSCRIPTION_ID" \
    --set-string "azure.resourceGroupName=$AZURE_RESOURCE_GROUP_NAME" \
    --set-string "azure.location=$AZURE_LOCATION" \
    --set-string "azure.credentials.existingSecret=$operator_credentials_secret" \
    --rollback-on-failure \
    --wait \
    --timeout "$wait_timeout"; then
    dump_operator_diagnostics
    exit 1
  fi
}

verify_operator_release() {
  local ca_bundle
  local deadline
  local deployed_operator_image
  local failure_policies
  local pod
  local permission
  local scc
  local operator_service_account="system:serviceaccount:$operator_namespace:azure-workload-identity-operator-controller-manager"
  local webhook_service_account="system:serviceaccount:$webhook_namespace:azure-wi-webhook-admin"

  log WATCH "Waiting for the packaged operator and Azure workload identity webhook"
  oc rollout status deployment/azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" --timeout="$wait_timeout"
  oc rollout status deployment/azure-wi-webhook-controller-manager \
    -n "$webhook_namespace" --timeout="$wait_timeout"
  oc wait certificate/azure-workload-identity-operator-serving-cert \
    -n "$operator_namespace" --for=condition=Ready --timeout="$wait_timeout"
  oc wait certificate/azure-wi-webhook-serving-cert \
    -n "$webhook_namespace" --for=condition=Ready --timeout="$wait_timeout"
  if [[ -n $operator_image_digest ]]; then
    deployed_operator_image=$(oc get deployment azure-workload-identity-operator-controller-manager \
      -n "$operator_namespace" -o jsonpath='{.spec.template.spec.containers[?(@.name=="manager")].image}')
    [[ $deployed_operator_image == "$operator_image_repository@$operator_image_digest" ]] || \
      die "operator Deployment uses $deployed_operator_image, expected the validated candidate digest"
  fi

  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  log WATCH "Waiting for cert-manager CA injection into the validating webhooks"
  while ((SECONDS < deadline)); do
    ca_bundle=$(oc get validatingwebhookconfiguration \
      azure-workload-identity-operator-validating-webhook-configuration \
      -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
    [[ -n $ca_bundle ]] && break
    sleep_until_deadline "$deadline" 2 || true
  done
  [[ -n $ca_bundle ]] || die "operator ValidatingWebhookConfiguration has no injected CA bundle"

  ca_bundle=""
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  log WATCH "Waiting for cert-manager CA injection into the Azure workload identity mutating webhook"
  while ((SECONDS < deadline)); do
    ca_bundle=$(oc get mutatingwebhookconfiguration \
      azure-wi-webhook-mutating-webhook-configuration \
      -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
    [[ -n $ca_bundle ]] && break
    sleep_until_deadline "$deadline" 2 || true
  done
  [[ -n $ca_bundle ]] || die "Azure workload identity MutatingWebhookConfiguration has no injected CA bundle"
  failure_policies=$(oc get validatingwebhookconfiguration \
    azure-workload-identity-operator-validating-webhook-configuration \
    -o jsonpath='{range .webhooks[*]}{.failurePolicy}{"\n"}{end}')
  if [[ $(printf '%s\n' "$failure_policies" | grep -c '^Fail$') -ne 3 ]]; then
    die "all three operator validating webhooks must use failurePolicy=Fail"
  fi

  permission=$(oc auth can-i get secrets -n "$webhook_namespace" --as="$webhook_service_account" || true)
  [[ $permission == "no" ]] || die "Azure workload identity webhook ServiceAccount can read Secrets"
  permission=$(oc auth can-i update mutatingwebhookconfigurations.admissionregistration.k8s.io \
    --as="$webhook_service_account" || true)
  [[ $permission == "no" ]] || die "Azure workload identity webhook ServiceAccount can update MutatingWebhookConfigurations"

  permission=$(oc auth can-i get "secret/$SIGNING_KEY_SECRET_NAME" \
    -n "$SIGNING_KEY_SECRET_NAMESPACE" --as="$operator_service_account")
  [[ $permission == "yes" ]] || die "operator ServiceAccount cannot get the configured signing key Secret"
  permission=$(oc auth can-i list secrets -n "$operator_namespace" --as="$operator_service_account" || true)
  [[ $permission == "no" ]] || die "operator ServiceAccount can list Secrets"
  permission=$(oc auth can-i watch secrets -n "$operator_namespace" --as="$operator_service_account" || true)
  [[ $permission == "no" ]] || die "operator ServiceAccount can watch Secrets"

  while IFS= read -r pod; do
    [[ -z $pod ]] && continue
    scc=$(oc get pod "$pod" -n "$operator_namespace" \
      -o go-template='{{index .metadata.annotations "openshift.io/scc"}}')
    case "$scc" in
      restricted|restricted-v2) ;;
      *) die "Pod/$pod was admitted with SCC ${scc:-<empty>}, expected restricted or restricted-v2" ;;
    esac
  done < <(oc get pods -n "$operator_namespace" \
    -l 'control-plane=controller-manager' -o name | sed 's#pod/##')

  while IFS= read -r pod; do
    [[ -z $pod ]] && continue
    scc=$(oc get pod "$pod" -n "$webhook_namespace" \
      -o go-template='{{index .metadata.annotations "openshift.io/scc"}}')
    case "$scc" in
      restricted|restricted-v2) ;;
      *) die "Pod/$pod was admitted with SCC ${scc:-<empty>}, expected restricted or restricted-v2" ;;
    esac
  done < <(oc get pods -n "$webhook_namespace" \
    -l 'azure-workload-identity.io/system=true' -o name | sed 's#pod/##')

  log VERIFY "Helm release, cert-manager certificates, least-privilege fail-closed webhooks, and OpenShift restricted SCC are Ready"
}

dump_operator_diagnostics() {
  helm status "$operator_release" -n "$operator_namespace" >&2 || true
  dump_namespaced_resources "$operator_namespace"
  dump_namespaced_resources "$webhook_namespace"
  oc get certificates.cert-manager.io,issuers.cert-manager.io -n "$operator_namespace" >&2 || true
  oc get certificates.cert-manager.io,issuers.cert-manager.io -n "$webhook_namespace" >&2 || true
  oc describe pods -n "$operator_namespace" >&2 || true
  oc describe pods -n "$webhook_namespace" >&2 || true
  oc logs -n "$operator_namespace" deployment/azure-workload-identity-operator-controller-manager \
    --all-containers --tail=200 >&2 || true
  oc logs -n "$webhook_namespace" deployment/azure-wi-webhook-controller-manager \
    --all-containers --tail=200 >&2 || true
  oc get validatingwebhookconfigurations,mutatingwebhookconfigurations >&2 || true
  oc get events -n "$operator_namespace" --sort-by=.lastTimestamp >&2 || true
  oc get events -n "$webhook_namespace" --sort-by=.lastTimestamp >&2 || true
}

dump_namespaced_resources() {
  local namespace=$1

  oc get \
    deployments.apps,replicasets.apps,statefulsets.apps,daemonsets.apps,pods,services,jobs.batch,cronjobs.batch,poddisruptionbudgets.policy \
    -n "$namespace" >&2 || true
}

ensure_key_vault_exists() {
  local deleted_vault_id
  local existing_vault_id
  local resource_group_status

  if [[ $ensure_key_vault != "true" ]]; then
    return
  fi

  log VERIFY "Ensuring Key Vault $KEY_VAULT_NAME exists in resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
  if ! existing_vault_id=$(az keyvault list \
    --query "[?name=='$KEY_VAULT_NAME'].id | [0]" \
    -o tsv 2>&1); then
    die "Could not check for an existing Key Vault named $KEY_VAULT_NAME: $existing_vault_id"
  fi
  if [[ -n $existing_vault_id ]]; then
    die "Key Vault $KEY_VAULT_NAME already exists; choose another KEY_VAULT_NAME"
  fi
  if ! deleted_vault_id=$(az keyvault list-deleted \
    --query "[?name=='$KEY_VAULT_NAME'].id | [0]" \
    -o tsv 2>&1); then
    die "Could not check for a soft-deleted Key Vault named $KEY_VAULT_NAME: $deleted_vault_id"
  fi
  if [[ -n $deleted_vault_id ]]; then
    die "A recoverable soft-deleted Key Vault named $KEY_VAULT_NAME already exists; choose another KEY_VAULT_NAME"
  fi

  if azure_resource_group_exists "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"; then
    die "Azure resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME appeared after the preflight check; refusing to adopt it"
  else
    resource_group_status=$?
    if [[ $resource_group_status -ne 1 ]]; then
      die "Could not check Azure resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME before creation: $azure_resource_group_check_error"
    fi
  fi
  log CREATE "Creating Key Vault resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
  az group create -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" -l "$AZURE_LOCATION" -o none
  created_key_vault_resource_group=true

  log CREATE "Creating Key Vault $KEY_VAULT_NAME"
  if ! az keyvault create \
    -n "$KEY_VAULT_NAME" \
    -g "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" \
    -l "$AZURE_LOCATION" \
    --retention-days 7 \
    --enable-rbac-authorization true \
    -o none; then
    die "Failed to create Key Vault $KEY_VAULT_NAME. The name may be globally unavailable; set KEY_VAULT_NAME to another value."
  fi
  created_key_vault=true
  vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv)

  if [[ $enable_key_vault_rbac == "true" ]]; then
    key_vault_rbac_enabled=$(az keyvault show -n "$KEY_VAULT_NAME" --query properties.enableRbacAuthorization -o tsv)
    case "$key_vault_rbac_enabled" in
      true|True|TRUE) ;;
      *)
        log UPDATE "Enabling Azure RBAC authorization on Key Vault $KEY_VAULT_NAME"
        az keyvault update --id "$vault_id" --enable-rbac-authorization true -o none
        ;;
    esac
  fi
}

decode_base64url() {
  local value=$1
  local remainder
  remainder=$((${#value} % 4))

  case "$remainder" in
    0) ;;
    2) value="${value}==" ;;
    3) value="${value}=" ;;
    *) return 1 ;;
  esac

  value=${value//-/+}
  value=${value//_/\/}

  if printf '%s' "$value" | base64 --decode 2>/dev/null; then
    return
  fi
  if printf '%s' "$value" | base64 -d 2>/dev/null; then
    return
  fi
  printf '%s' "$value" | base64 -D 2>/dev/null
}

jwt_issuer() {
  local token=$1
  local payload
  local decoded
  payload=${token#*.}
  payload=${payload%%.*}
  if [[ -z $payload || $payload == "$token" ]]; then
    return 1
  fi

  decoded=$(decode_base64url "$payload") || return 1
  printf '%s\n' "$decoded" | sed -n 's/.*"iss"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

wait_for_service_account_token_issuer() {
  local issuer_url=$1
  local timeout=$2
  local deadline
  local token
  local token_issuer
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for newly issued service account tokens to use issuer $issuer_url"
  while ((SECONDS < deadline)); do
    if ! oc get serviceaccount default -n "$NAMESPACE" >/dev/null 2>&1; then
      sleep_until_deadline "$deadline" 10 || true
      continue
    fi

    token=$(oc create token default -n "$NAMESPACE" --duration=10m 2>/dev/null || true)
    if [[ -n $token ]]; then
      token_issuer=$(jwt_issuer "$token" || true)
      if [[ $token_issuer == "$issuer_url" ]]; then
        log VERIFY "OpenShift API server is issuing service account tokens with issuer $issuer_url"
        return
      fi
      if [[ -n $token_issuer ]]; then
        log WATCH "Current service account token issuer is $token_issuer; waiting for $issuer_url"
      fi
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for service account tokens to use issuer $issuer_url"
  oc get clusteroperator kube-apiserver >&2 || true
  oc describe clusteroperator kube-apiserver >&2 || true
  oc get authentication.config.openshift.io cluster -o yaml >&2 || true
  return 1
}

blocking_scheduling_taints() {
  local taints=$1
  local taint
  local effect
  local blocking_taints=()
  local taint_list=()

  IFS=',' read -ra taint_list <<<"$taints"
  for taint in "${taint_list[@]}"; do
    [[ -z $taint ]] && continue
    effect=${taint##*=}
    if [[ $effect == "NoSchedule" || $effect == "NoExecute" ]]; then
      blocking_taints+=("$taint")
    fi
  done

  printf '%s' "${blocking_taints[*]}"
}

wait_for_schedulable_node() {
  local timeout=$1
  local deadline
  local node_lines
  local node_name
  local ready_status
  local taints
  local blocking_taints
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for an OpenShift node to be Ready and schedulable"
  while ((SECONDS < deadline)); do
    node_lines=$(oc get nodes -o go-template='{{range .items}}{{.metadata.name}}{{"\t"}}{{range .status.conditions}}{{if eq .type "Ready"}}{{.status}}{{end}}{{end}}{{"\t"}}{{range .spec.taints}}{{.key}}={{.effect}}{{","}}{{end}}{{"\n"}}{{end}}' 2>/dev/null || true)
    while IFS=$'\t' read -r node_name ready_status taints; do
      [[ -z $node_name ]] && continue
      blocking_taints=$(blocking_scheduling_taints "$taints")
      if [[ $ready_status == "True" && -z $blocking_taints ]]; then
        log VERIFY "OpenShift node $node_name is Ready and schedulable"
        return
      fi
    done <<<"$node_lines"
    sleep_until_deadline "$deadline" 10 || true
  done

  log ERROR "Timed out waiting for an OpenShift node to be Ready without NoSchedule/NoExecute taints"
  oc get nodes -o wide >&2 || true
  oc describe nodes >&2 || true
  return 1
}

prepare_key_vault_secret_reader_build_context() {
  local reader_dir=$1
  local build_context=$2
  local node_arch

  if [[ ! -f "$reader_dir/main.go" || ! -f "$reader_dir/go.mod" || ! -f "$reader_dir/go.sum" ]]; then
    die "Missing Key Vault secret reader app sources at $reader_dir"
  fi

  node_arch=$(oc get nodes -o jsonpath='{.items[0].status.nodeInfo.architecture}')
  if [[ -z $node_arch ]]; then
    die "Could not determine OpenShift node architecture"
  fi

  mkdir -p "$build_context"
  log BUILD "Compiling Key Vault secret reader locally for linux/$node_arch"
  (
    cd "$reader_dir"
    CGO_ENABLED=0 GOOS=linux GOARCH="$node_arch" go build -o "$build_context/keyvault-secret-reader" .
  )
  chmod 0555 "$build_context/keyvault-secret-reader"

  cat >"$build_context/Dockerfile" <<'EOF'
FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
COPY keyvault-secret-reader /keyvault-secret-reader
USER nonroot:nonroot
ENTRYPOINT ["/keyvault-secret-reader"]
EOF
}

openshift_service_account_issuer() {
  oc get authentication.config.openshift.io cluster -o jsonpath='{.spec.serviceAccountIssuer}'
}

wait_for_openshift_authentication_service_account_issuer() {
  local issuer_url=$1
  local timeout=$2
  local deadline
  local configured_issuer
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OpenShift Authentication serviceAccountIssuer to become ${issuer_url:-<empty>}"
  while ((SECONDS < deadline)); do
    if configured_issuer=$(openshift_service_account_issuer 2>/dev/null); then
      if [[ $configured_issuer == "$issuer_url" ]]; then
        return
      fi
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for Authentication/cluster serviceAccountIssuer to become ${issuer_url:-<empty>}"
  oc get authentication.config.openshift.io cluster -o yaml >&2 || true
  return 1
}

wait_for_openshift_kube_apiserver_operator() {
  local timeout=$1

  log WATCH "Waiting for OpenShift kube-apiserver operator rollout"
  wait_for_openshift_condition clusteroperator/kube-apiserver Available=True "$timeout" || return 1
  wait_for_openshift_condition clusteroperator/kube-apiserver Progressing=False "$timeout" || return 1
  wait_for_openshift_condition clusteroperator/kube-apiserver Degraded=False "$timeout" || return 1
}

wait_for_openshift_auth_operators() {
  local timeout=$1
  local operator

  for operator in authentication openshift-apiserver; do
    log WATCH "Waiting for OpenShift $operator operator health"
    wait_for_openshift_condition "clusteroperator/$operator" Available=True "$timeout" || return 1
    wait_for_openshift_condition "clusteroperator/$operator" Progressing=False "$timeout" || return 1
    wait_for_openshift_condition "clusteroperator/$operator" Degraded=False "$timeout" || return 1
  done
}

wait_for_openshift_condition() {
  local resource=$1
  local condition=$2
  local timeout=$3
  local deadline
  local remaining
  local attempt_timeout
  local output
  local status
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while true; do
    remaining=$((deadline - SECONDS))
    if (( remaining <= 0 )); then
      log ERROR "Timed out waiting for $resource condition $condition"
      return 1
    fi

    attempt_timeout=$remaining
    if (( attempt_timeout > 30 )); then
      attempt_timeout=30
    fi

    if output=$(oc wait "$resource" --for="condition=$condition" --timeout="${attempt_timeout}s" 2>&1); then
      [[ -n $output ]] && printf '%s\n' "$output"
      return 0
    else
      status=$?
    fi

    if (( SECONDS >= deadline )); then
      [[ -n $output ]] && log ERROR "$output"
      return "$status"
    fi

    if is_transient_kubernetes_error "$output" || [[ $output == *"timed out waiting for the condition"* ]]; then
      log RETRY "OpenShift API/operator condition $resource $condition is not stable yet; retrying"
      sleep_until_deadline "$deadline" 10 || true
      continue
    fi

    [[ -n $output ]] && printf '%s\n' "$output" >&2
    return "$status"
  done
}

wait_for_openshift_api_server_rollout() {
  local issuer_url=$1
  local timeout=${2:-${OPENSHIFT_API_SERVER_ROLLOUT_TIMEOUT:-$wait_timeout}}

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" ]]; then
    return
  fi

  wait_for_openshift_authentication_service_account_issuer "$issuer_url" "$timeout" || return 1
  wait_for_openshift_kube_apiserver_operator "$timeout" || return 1
  wait_for_openshift_auth_operators "$timeout" || return 1
  if [[ -n $issuer_url ]]; then
    wait_for_service_account_token_issuer "$issuer_url" "$timeout" || return 1
  else
    log SKIP "Skipping service-account token issuer claim check because the target configured issuer is empty"
  fi

  log WATCH "Waiting for OpenShift kube-apiserver operator to settle after token issuer update"
  wait_for_openshift_kube_apiserver_operator "$timeout" || return 1
  wait_for_openshift_auth_operators "$timeout" || return 1
}

wait_for_openshift_oauth_apis() {
  local timeout=$1
  local deadline
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OpenShift OAuth APIs"
  while ((SECONDS < deadline)); do
    if oc get --raw /apis/oauth.openshift.io/v1 >/dev/null 2>&1 && \
      oc get --raw /apis/user.openshift.io/v1 >/dev/null 2>&1; then
      log VERIFY "OpenShift OAuth APIs are available"
      return
    fi
    sleep_until_deadline "$deadline" 10 || true
  done

  log ERROR "Timed out waiting for OpenShift OAuth APIs"
  oc get clusteroperator authentication console openshift-apiserver >&2 || true
  return 1
}

refresh_openshift_oauth_apiserver_after_issuer_handoff() {
  local timeout=$1
  local expected_issuer
  local oauth_pod
  local pod_token
  local pod_token_issuer

  expected_issuer=$original_openshift_service_account_token_issuer
  if [[ -z $expected_issuer ]]; then
    log ERROR "Original OpenShift service-account token issuer was not captured"
    return 1
  fi

  wait_for_service_account_token_issuer "$expected_issuer" "$timeout" || return 1

  oauth_pod=$(oc get pod -n openshift-oauth-apiserver -l app=openshift-oauth-apiserver \
    -o jsonpath='{.items[0].metadata.name}') || return 1
  if [[ -z $oauth_pod ]]; then
    log ERROR "OpenShift OAuth API server Pod was not found"
    return 1
  fi

  # This is deterministic e2e cleanup, not a test of natural OpenShift recovery.
  # The Pod can retain a token from the previous issuer until kubelet rotates it,
  # which can take tens of minutes. New tokens use the restored issuer at this point,
  # so recreating the Pod gives it a valid token without slowing every e2e run.
  log UPDATE "Restarting OpenShift OAuth API server to refresh its service-account token after issuer handoff"
  oc delete pod "$oauth_pod" -n openshift-oauth-apiserver --wait=false >/dev/null || return 1
  wait_for_kubernetes_resource_absent "pod/$oauth_pod" openshift-oauth-apiserver "$timeout" || return 1
  wait_for_openshift_oauth_apis "$timeout" || return 1

  pod_token=$(oc exec -n openshift-oauth-apiserver deployment/apiserver -- \
    cat /var/run/secrets/kubernetes.io/serviceaccount/token 2>/dev/null) || {
    log ERROR "Failed to read the refreshed OpenShift OAuth API server service-account token"
    return 1
  }
  pod_token_issuer=$(jwt_issuer "$pod_token" || true)
  if [[ $pod_token_issuer != "$expected_issuer" ]]; then
    log ERROR "OpenShift OAuth API server token issuer is ${pod_token_issuer:-<empty>}; expected $expected_issuer"
    return 1
  fi
  log VERIFY "OpenShift OAuth API server token uses the restored issuer $expected_issuer"

  wait_for_openshift_auth_operators "$timeout" || return 1
  wait_for_openshift_condition clusteroperator/console Available=True "$timeout" || return 1
  wait_for_openshift_condition clusteroperator/console Progressing=False "$timeout" || return 1
  wait_for_openshift_condition clusteroperator/console Degraded=False "$timeout" || return 1
}

refresh_operator_after_issuer_handoff() {
  local timeout=$1

  # The operator's projected service-account token can still carry the previous
  # issuer after OpenShift has completed the handoff. Restart it before invoking
  # its fail-closed validating webhook during OIDCIssuer cleanup.
  log UPDATE "Restarting the packaged operator to refresh its service-account token after issuer handoff"
  oc rollout restart deployment/azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" >/dev/null || return 1
  if ! oc rollout status deployment/azure-workload-identity-operator-controller-manager \
    -n "$operator_namespace" --timeout="$timeout"; then
    log ERROR "Packaged operator did not become Ready after the issuer handoff"
    return 1
  fi
}

capture_original_openshift_service_account_issuer() {
  local token

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" ]]; then
    return
  fi

  original_openshift_service_account_issuer=$(openshift_service_account_issuer)
  token=$(oc create token default -n default --duration=10m) || {
    log ERROR "Failed to create a service-account token while capturing the original OpenShift issuer"
    return 1
  }
  original_openshift_service_account_token_issuer=$(jwt_issuer "$token") || {
    log ERROR "Failed to capture the original OpenShift service-account token issuer"
    return 1
  }
  captured_original_openshift_service_account_issuer=true
  log CAPTURE "Captured original OpenShift serviceAccountIssuer: ${original_openshift_service_account_issuer:-<empty>}"
  log CAPTURE "Captured original OpenShift service-account token issuer: $original_openshift_service_account_token_issuer"
}

verify_oidcissuer_captured_previous_service_account_issuer() {
  local issuer_url=$1
  local previous_present
  local previous_issuer

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi
  if [[ $original_openshift_service_account_issuer == "$issuer_url" ]]; then
    die "OpenShift serviceAccountIssuer already matched the published issuer; use a clean CRC cluster or unique issuer storage to exercise previous issuer capture"
  fi

  # $k and $v are Go template variables.
  # shellcheck disable=SC2016
  previous_present=$(oc get oidcissuer default -o go-template='{{range $k, $v := .status}}{{if eq $k "previousServiceAccountIssuer"}}true{{end}}{{end}}')
  if [[ $previous_present != "true" ]]; then
    die "OIDCIssuer/default did not record status.previousServiceAccountIssuer"
  fi

  previous_issuer=$(oc get oidcissuer default -o go-template='{{index .status "previousServiceAccountIssuer"}}')
  if [[ $previous_issuer != "$original_openshift_service_account_issuer" ]]; then
    die "OIDCIssuer/default status.previousServiceAccountIssuer is '$previous_issuer', want '${original_openshift_service_account_issuer:-<empty>}'"
  fi
}

create_retiring_signing_key_secret() {
  if [[ $verify_signing_key_rotation != "true" ]]; then
    return
  fi

  apply_generated_signing_key_secret \
    "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" \
    "$RETIRING_SIGNING_KEY_SECRET_NAME" \
    "$RETIRING_SIGNING_KEY_SECRET_KEY" \
    "retiring-signing-key"
  created_retiring_signing_key_secret=true
}

apply_generated_signing_key_secret() {
  local namespace=$1
  local name=$2
  local key=$3
  local file_prefix=$4
  local private_key_file
  local public_key_file

  private_key_file="$tmpdir/$file_prefix.pem"
  public_key_file="$tmpdir/$file_prefix.pub"

  log CREATE "Creating signing key Secret $namespace/$name"
  openssl genrsa -out "$private_key_file" 2048 >/dev/null 2>&1
  openssl rsa -in "$private_key_file" -pubout -out "$public_key_file" >/dev/null 2>&1
  oc create secret generic "$name" \
    -n "$namespace" \
    --from-file="$key=$public_key_file" \
    --dry-run=client \
    -o yaml | oc apply -f - >/dev/null
}

wait_for_oidcissuer_signing_key_states() {
  local expected_active_count=$1
  local expected_retiring_count=$2
  local timeout=$3
  local deadline
  local active_count
  local retiring_count
  local signing_keys
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OIDCIssuer/default to publish $expected_active_count active and $expected_retiring_count retiring signing key(s)"
  while ((SECONDS < deadline)); do
    signing_keys=$(oc get oidcissuer default -o jsonpath='{range .status.signingKeys[*]}{.state}{"\n"}{end}' 2>/dev/null || true)
    active_count=$(printf '%s\n' "$signing_keys" | grep -c '^Active$' || true)
    retiring_count=$(printf '%s\n' "$signing_keys" | grep -c '^Retiring$' || true)
    if [[ $active_count == "$expected_active_count" && $retiring_count == "$expected_retiring_count" ]]; then
      log VERIFY "OIDCIssuer/default status.signingKeys contains expected Active/Retiring states"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for OIDCIssuer/default status.signingKeys to contain expected Active/Retiring states"
  oc get oidcissuer default -o yaml >&2 || true
  return 1
}

wait_for_jwks_key_count() {
  local issuer_url=$1
  local expected_count=$2
  local timeout=$3
  local deadline
  local jwks
  local key_count
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for JWKS at $issuer_url/openid/v1/jwks to contain $expected_count key(s)"
  while ((SECONDS < deadline)); do
    jwks=$(curl -fsS "$issuer_url/openid/v1/jwks" 2>/dev/null || true)
    if [[ -n $jwks ]]; then
      key_count=$(printf '%s\n' "$jwks" | grep -o '"kid"' | wc -l | tr -d '[:space:]')
      if [[ $key_count == "$expected_count" ]]; then
        log VERIFY "JWKS contains $expected_count signing key(s)"
        return
      fi
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for JWKS to contain $expected_count key(s)"
  [[ -n ${jwks:-} ]] && printf '%s\n' "$jwks" >&2
  return 1
}

oidcissuer_retiring_key_id() {
  oc get oidcissuer default -o jsonpath='{range .status.signingKeys[?(@.state=="Retiring")]}{.kid}{"\n"}{end}' 2>/dev/null | sed -n '1p'
}

wait_for_retiring_signing_key_periodic_refresh() {
  local issuer_url=$1
  local previous_kid=$2
  local expected_generation=$3
  local timeout=$4
  local deadline
  local generation
  local current_kid
  local jwks
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OIDCIssuer/default periodic refresh to publish the replaced retiring signing key"
  while ((SECONDS < deadline)); do
    generation=$(oc get oidcissuer default -o jsonpath='{.metadata.generation}' 2>/dev/null || true)
    if [[ -n $generation && $generation != "$expected_generation" ]]; then
      log ERROR "OIDCIssuer/default generation changed from $expected_generation to $generation while verifying periodic refresh"
      oc get oidcissuer default -o yaml >&2 || true
      return 1
    fi

    current_kid=$(oidcissuer_retiring_key_id)
    if [[ -n $current_kid && $current_kid != "$previous_kid" ]]; then
      jwks=$(curl -fsS "$issuer_url/openid/v1/jwks" 2>/dev/null || true)
      if [[ $jwks == *"\"kid\":\"$current_kid\""* || $jwks == *"\"kid\": \"$current_kid\""* ]]; then
        log VERIFY "OIDCIssuer/default periodic refresh republished retiring signing key $current_kid without an OIDCIssuer spec update"
        return
      fi
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for periodic refresh to publish replaced retiring signing key"
  oc get oidcissuer default -o yaml >&2 || true
  curl -fsS "$issuer_url/openid/v1/jwks" >&2 || true
  return 1
}

verify_oidc_issuer_periodic_refresh_publish() {
  local issuer_url=$1
  local previous_kid
  local generation

  if [[ $verify_oidc_issuer_periodic_refresh != "true" ]]; then
    return
  fi
  if [[ $verify_signing_key_rotation != "true" ]]; then
    log SKIP "Skipping OIDCIssuer periodic refresh verification because signing key rotation verification is disabled"
    return
  fi

  previous_kid=$(oidcissuer_retiring_key_id)
  if [[ -z $previous_kid ]]; then
    log ERROR "OIDCIssuer/default has no retiring signing key to replace for periodic refresh verification"
    oc get oidcissuer default -o yaml >&2 || true
    return 1
  fi
  generation=$(oc get oidcissuer default -o jsonpath='{.metadata.generation}')

  log UPDATE "Replacing retiring signing key Secret $RETIRING_SIGNING_KEY_SECRET_NAMESPACE/$RETIRING_SIGNING_KEY_SECRET_NAME without changing OIDCIssuer/default"
  apply_generated_signing_key_secret \
    "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" \
    "$RETIRING_SIGNING_KEY_SECRET_NAME" \
    "$RETIRING_SIGNING_KEY_SECRET_KEY" \
    "periodic-refresh-retiring-signing-key"

  wait_for_retiring_signing_key_periodic_refresh "$issuer_url" "$previous_kid" "$generation" "$wait_timeout" || return 1
}

verify_signing_key_rotation_publish() {
  local issuer_url=$1

  if [[ $verify_signing_key_rotation != "true" ]]; then
    return
  fi

  create_retiring_signing_key_secret
  log PATCH "Adding retiring signing key reference to OIDCIssuer/default"
  oc patch oidcissuer default --type=merge -p "{\"spec\":{\"signingKey\":{\"retiringSecretRef\":{\"namespace\":\"$RETIRING_SIGNING_KEY_SECRET_NAMESPACE\",\"name\":\"$RETIRING_SIGNING_KEY_SECRET_NAME\",\"key\":\"$RETIRING_SIGNING_KEY_SECRET_KEY\"}}}}" >/dev/null
  wait_for_oidcissuer_observed_generation "$wait_timeout" || return 1
  oc wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout" >/dev/null || return 1
  wait_for_oidcissuer_signing_key_states 1 1 "$wait_timeout" || return 1
  wait_for_jwks_key_count "$issuer_url" 2 "$wait_timeout" || return 1
}

patch_openshift_service_account_issuer() {
  local issuer_url=$1

  if [[ -n $issuer_url ]]; then
    oc patch authentication.config.openshift.io cluster --type=merge -p "{\"spec\":{\"serviceAccountIssuer\":\"$issuer_url\"}}" >/dev/null
  else
    oc patch authentication.config.openshift.io cluster --type=merge -p '{"spec":{"serviceAccountIssuer":""}}' >/dev/null
  fi
}

wait_for_oidcissuer_observed_generation() {
  local timeout=$1
  local deadline
  local generation
  local observed_generation

  generation=$(oc get oidcissuer default -o jsonpath='{.metadata.generation}') || return 1
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OIDCIssuer/default observedGeneration to reach $generation"
  while ((SECONDS < deadline)); do
    observed_generation=$(oc get oidcissuer default -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)
    if [[ $observed_generation =~ ^[0-9]+$ ]] && ((observed_generation >= generation)); then
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for OIDCIssuer/default observedGeneration to reach $generation"
  oc get oidcissuer default -o yaml >&2 || true
  return 1
}

verify_openshift_service_account_issuer_handed_off() {
  local current_issuer
  local issuer_url

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi

  issuer_url=$(oc get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
  if [[ -z $issuer_url ]]; then
    return
  fi

  current_issuer=$(openshift_service_account_issuer) || return 1
  if [[ $current_issuer == "$issuer_url" ]]; then
    log ERROR "OpenShift Authentication serviceAccountIssuer still references $issuer_url; refusing to delete OIDCIssuer/default"
    return 1
  fi
}

handoff_openshift_service_account_issuer_before_oidcissuer_delete() {
  local current_issuer
  local issuer_url
  local timeout=${OPENSHIFT_API_SERVER_ROLLOUT_TIMEOUT:-$wait_timeout}

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi

  issuer_url=$(oc get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
  if [[ -z $issuer_url ]]; then
    log SKIP "Skipping OpenShift serviceAccountIssuer handoff because OIDCIssuer/default has no status.issuerURL"
    return
  fi

  if ! current_issuer=$(openshift_service_account_issuer); then
    log ERROR "Failed to read OpenShift Authentication serviceAccountIssuer before OIDCIssuer deletion"
    return 1
  fi
  if [[ $current_issuer != "$issuer_url" ]]; then
    log SKIP "Skipping OpenShift serviceAccountIssuer handoff because Authentication/cluster no longer references $issuer_url"
    return
  fi

  log UPDATE "Disabling OIDCIssuer OpenShift serviceAccountIssuer management before manual handoff"
  oc patch oidcissuer default --type=merge -p '{"spec":{"openShift":{"updateServiceAccountIssuer":false}}}' >/dev/null || return 1
  wait_for_oidcissuer_observed_generation "$timeout" || return 1

  log UPDATE "Restoring OpenShift Authentication serviceAccountIssuer to ${original_openshift_service_account_issuer:-<empty>} before deleting OIDCIssuer"
  patch_openshift_service_account_issuer "$original_openshift_service_account_issuer" || return 1

  log WATCH "Waiting for OpenShift API server rollout after manual serviceAccountIssuer handoff"
  wait_for_openshift_api_server_rollout "$original_openshift_service_account_issuer" "$timeout" || return 1
  refresh_openshift_oauth_apiserver_after_issuer_handoff "$timeout" || return 1
  refresh_operator_after_issuer_handoff "$timeout" || return 1
  verify_openshift_service_account_issuer_handed_off || return 1
}

assert_oidcissuer_delete_rejected_by_workload_identity() {
  local delete_output
  local deletion_timestamp

  if [[ $applied_oidc_issuer != "true" || $applied_workload_identity != "true" ]]; then
    return
  fi

  log VERIFY "Verifying OIDCIssuer deletion is rejected while WorkloadIdentity exists"
  if delete_output=$(oc delete oidcissuer default --wait=false 2>&1); then
    log ERROR "OIDCIssuer/default deletion was accepted while WorkloadIdentity/$WORKLOAD_IDENTITY_NAME still exists"
    oc get oidcissuer default -o yaml >&2 || true
    oc get workloadidentities -A >&2 || true
    return 1
  fi
  if [[ $delete_output != *"OIDCIssuer deletion is blocked"* ]]; then
    log ERROR "OIDCIssuer/default deletion failed for an unexpected reason: $delete_output"
    return 1
  fi

  deletion_timestamp=$(oc get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
  if [[ -n $deletion_timestamp ]]; then
    log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though deletion was rejected"
    oc get oidcissuer default -o yaml >&2 || true
    return 1
  fi

  log VERIFY "OIDCIssuer deletion was rejected before the resource entered deletion"
}

assert_oidcissuer_delete_rejected_by_openshift_service_account_issuer() {
  local current_issuer
  local delete_output
  local deletion_timestamp
  local issuer_url

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi

  issuer_url=$(oc get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
  if [[ -z $issuer_url ]]; then
    log SKIP "Skipping OIDCIssuer OpenShift handoff guard check because status.issuerURL is empty"
    return
  fi

  current_issuer=$(openshift_service_account_issuer 2>/dev/null || true)
  if [[ $current_issuer != "$issuer_url" ]]; then
    log SKIP "Skipping OIDCIssuer OpenShift handoff guard check because Authentication/cluster no longer references $issuer_url"
    return
  fi

  log VERIFY "Verifying OIDCIssuer deletion is rejected while OpenShift serviceAccountIssuer references $issuer_url"
  if delete_output=$(oc delete oidcissuer default --wait=false 2>&1); then
    log ERROR "OIDCIssuer/default deletion was accepted while OpenShift serviceAccountIssuer still references $issuer_url"
    oc get oidcissuer default -o yaml >&2 || true
    oc get authentication.config.openshift.io cluster -o yaml >&2 || true
    return 1
  fi
  if [[ $delete_output != *"cluster is still minting service account tokens with issuer \"$issuer_url\""* ]]; then
    log ERROR "OIDCIssuer/default deletion failed for an unexpected reason: $delete_output"
    return 1
  fi

  deletion_timestamp=$(oc get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
  if [[ -n $deletion_timestamp ]]; then
    log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though deletion was rejected"
    oc get oidcissuer default -o yaml >&2 || true
    return 1
  fi

  log VERIFY "OIDCIssuer deletion was rejected before OpenShift serviceAccountIssuer handoff"
}

wait_for_workloadidentity_ready_false_reason() {
  local name=$1
  local reason=$2
  local timeout=$3
  local deadline
  local condition
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for WorkloadIdentity/$name Ready=False reason $reason"
  while ((SECONDS < deadline)); do
    condition=$(oc get workloadidentity "$name" -n "$NAMESPACE" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{"|"}{.reason}{"\n"}{end}' 2>/dev/null || true)
    if [[ $condition == *"False|$reason"* ]]; then
      log VERIFY "WorkloadIdentity/$name reported Ready=False reason $reason"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for WorkloadIdentity/$name Ready=False reason $reason"
  oc get workloadidentity "$name" -n "$NAMESPACE" -o yaml >&2 || true
  return 1
}

verify_workload_identity_conflict_reconciliation() {
  local conflict_name=azwi-sa-conflict
  local conflict_service_account=azwi-sa-conflict
  local federated_credential_conflict_name=azwi-fic-conflict
  local federated_credential_conflict_service_account=azwi-fic-conflict
  local admission_output

  if [[ $verify_workload_identity_conflicts != "true" ]]; then
    return
  fi

  log VERIFY "Verifying WorkloadIdentity reconciliation reports ServiceAccount ownership conflicts"
  oc create serviceaccount "$conflict_service_account" -n "$NAMESPACE" >/dev/null
  created_conflict_service_account=true
  oc label serviceaccount "$conflict_service_account" -n "$NAMESPACE" \
    azure.workload.identity/use=true \
    workloadidentity.azure.micosolutions.se/managed-by=azure-workload-identity-operator \
    workloadidentity.azure.micosolutions.se/workload-identity-uid=foreign-workload-identity-uid \
    workloadidentity.azure.micosolutions.se/created-by-operator=false >/dev/null

  cat <<EOF | oc apply -f - >/dev/null
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: $conflict_name
  namespace: $NAMESPACE
spec:
  azure:
    userAssignedIdentityName: id-azwi-sa-conflict
    federatedIdentityCredentialName: fidc-conflict-safe-reconcile
  serviceAccount:
    name: $conflict_service_account
  deletionPolicy: Retain
EOF
  applied_conflict_workload_identity=true
  wait_for_workloadidentity_ready_false_reason "$conflict_name" ServiceAccountConflict "$wait_timeout" || return 1

  log VERIFY "Verifying API-server admission rejects a second owner for an existing Azure identity"
  if admission_output=$(cat <<EOF | oc apply -f - 2>&1
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: $federated_credential_conflict_name
  namespace: $NAMESPACE
spec:
  azure:
    userAssignedIdentityName: $AZURE_USER_ASSIGNED_IDENTITY_NAME
    federatedIdentityCredentialName: $AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME
  serviceAccount:
    name: $federated_credential_conflict_service_account
  deletionPolicy: Retain
EOF
  ); then
    log ERROR "API-server admission accepted a second WorkloadIdentity owner for the resolved Azure identity"
    applied_federated_credential_conflict_workload_identity=true
    return 1
  fi
  if [[ $admission_output != *"already referenced by WorkloadIdentity"* ]]; then
    log ERROR "Duplicate Azure identity admission failed for an unexpected reason: $admission_output"
    return 1
  fi
  log VERIFY "Real API-server admission rejected the duplicate Azure identity owner"
}

verify_workload_identity_immutable_name_and_tags() {
  local patch_output
  local configured_name
  local workload_identity_uid
  local expected_logical_key
  local created_by_operator
  local logical_key
  local tagged_uid
  local obsolete_federated_credential_name

  log VERIFY "Verifying WorkloadIdentity ServiceAccount name is immutable"
  if patch_output=$(oc patch workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --type=merge \
    -p "{\"spec\":{\"serviceAccount\":{\"name\":\"${SERVICE_ACCOUNT_NAME}-renamed\"}}}" 2>&1); then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME accepted a ServiceAccount name change"
    return 1
  fi
  if [[ $patch_output != *"field is immutable"* ]]; then
    log ERROR "ServiceAccount name update failed for an unexpected reason: $patch_output"
    return 1
  fi
  configured_name=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.serviceAccount.name}')
  if [[ $configured_name != "$SERVICE_ACCOUNT_NAME" ]]; then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME ServiceAccount name changed to $configured_name"
    return 1
  fi

  workload_identity_uid=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  expected_logical_key=$(printf '%s' "$NAMESPACE/$WORKLOAD_IDENTITY_NAME" | openssl dgst -sha256 -r | awk '{print $1}')
  created_by_operator=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."created-by-operator"' -o tsv)
  logical_key=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-key"' -o tsv)
  tagged_uid=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-uid"' -o tsv)
  obsolete_federated_credential_name=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."federated-credential-name"' -o tsv)
  if [[ $created_by_operator != "true" || $logical_key != "$expected_logical_key" || $tagged_uid != "$workload_identity_uid" ]]; then
    log ERROR "User assigned identity ownership tags do not match the WorkloadIdentity"
    return 1
  fi
  if [[ -n $obsolete_federated_credential_name ]]; then
    log ERROR "User assigned identity still has obsolete federated-credential-name tag"
    return 1
  fi
  log VERIFY "WorkloadIdentity immutable name and UAMI ownership tags are correct"
}

verify_workload_identity_service_account_recreation() {
  local initial_service_account_uid
  local workload_identity_state
  local workload_identity_uid
  local status_service_account_uid
  local provenance
  local expected_client_id
  local expected_tenant_id
  local create_output
  local create_attempt
  local manual_replacement_uid
  local replacement_created=false
  local deadline
  local service_account_state
  local current_service_account_uid
  local use_label
  local managed_by_label
  local owner_uid_label
  local created_by_label
  local client_id_annotation
  local tenant_id_annotation

  if [[ $verify_workload_identity_service_account_recreation_enabled != "true" ]]; then
    log SKIP "Skipping WorkloadIdentity ServiceAccount recreation verification"
    return 0
  fi

  initial_service_account_uid=$(oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  workload_identity_state=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o go-template='{{.metadata.uid}}{{"\t"}}{{.status.serviceAccountUID}}{{"\t"}}{{.status.serviceAccountProvenance}}{{"\t"}}{{.status.clientID}}{{"\t"}}{{.status.tenantID}}')
  IFS=$'\t' read -r workload_identity_uid status_service_account_uid provenance expected_client_id expected_tenant_id <<<"$workload_identity_state"

  if [[ -z $initial_service_account_uid || $status_service_account_uid != "$initial_service_account_uid" ]]; then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME status.serviceAccountUID does not match ServiceAccount/$SERVICE_ACCOUNT_NAME"
    return 1
  fi
  if [[ $provenance != "Created" ]]; then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME status.serviceAccountProvenance is '${provenance:-<empty>}', want 'Created'"
    return 1
  fi
  if [[ -z $workload_identity_uid || -z $expected_client_id ]]; then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME is missing metadata.uid or status.clientID"
    return 1
  fi
  if [[ $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" ]]; then
    expect_workload_identity_service_account_deleted=true
  fi
  log VERIFY "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME recorded Created provenance for ServiceAccount/$SERVICE_ACCOUNT_NAME"

  log PAUSE "Pausing the packaged operator so the script deterministically creates the replacement ServiceAccount"
  if ! pause_operator; then
    return 1
  fi

  log DELETE "Deleting ServiceAccount/$SERVICE_ACCOUNT_NAME to verify same-name recreation"
  if ! oc delete serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --wait=true --timeout="$wait_timeout" >/dev/null; then
    resume_operator || true
    return 1
  fi

  log CREATE "Recreating ServiceAccount/$SERVICE_ACCOUNT_NAME without managed metadata"
  for create_attempt in {1..10}; do
    if create_output=$(oc create serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" 2>&1); then
      replacement_created=true
      break
    fi
    if oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
      log WATCH "Operator recreated ServiceAccount/$SERVICE_ACCOUNT_NAME first; retrying manual replacement"
      if ! oc delete serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --wait=true --timeout="$wait_timeout" >/dev/null; then
        resume_operator || true
        return 1
      fi
      continue
    fi
    log ERROR "Could not recreate ServiceAccount/$SERVICE_ACCOUNT_NAME: $create_output"
    resume_operator || true
    return 1
  done
  if [[ $replacement_created != "true" ]]; then
    log ERROR "Could not win ServiceAccount/$SERVICE_ACCOUNT_NAME recreation race after $create_attempt attempts"
    resume_operator || true
    return 1
  fi

  if ! manual_replacement_uid=$(oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}'); then
    resume_operator || true
    return 1
  fi
  log UPDATE "Adding benign ServiceAccount metadata drift for reconciliation"
  if ! oc patch serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --type=merge -p '{"metadata":{"labels":{"e2e.azure.micosolutions.se/recreated":"true","workloadidentity.azure.micosolutions.se/created-by-operator":"false"}}}' >/dev/null; then
    resume_operator || true
    return 1
  fi
  log RUN "Resuming the packaged operator to reconcile the replacement ServiceAccount"
  if ! resume_operator; then
    return 1
  fi

  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  log WATCH "Waiting for WorkloadIdentity reconciliation to preserve provenance and repair ServiceAccount metadata"
  while ((SECONDS < deadline)); do
    workload_identity_state=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o go-template='{{.status.serviceAccountUID}}{{"\t"}}{{.status.serviceAccountProvenance}}' 2>/dev/null || true)
    IFS=$'\t' read -r status_service_account_uid provenance <<<"$workload_identity_state"
    service_account_state=$(oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o go-template='{{.metadata.uid}}{{"\t"}}{{index .metadata.labels "azure.workload.identity/use"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/managed-by"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/created-by-operator"}}{{"\t"}}{{index .metadata.annotations "azure.workload.identity/client-id"}}{{"\t"}}{{index .metadata.annotations "azure.workload.identity/tenant-id"}}' 2>/dev/null || true)
    IFS=$'\t' read -r current_service_account_uid use_label managed_by_label owner_uid_label created_by_label client_id_annotation tenant_id_annotation <<<"$service_account_state"

    if [[ -n $current_service_account_uid &&
      $current_service_account_uid == "$manual_replacement_uid" &&
      $current_service_account_uid != "$initial_service_account_uid" &&
      $status_service_account_uid == "$current_service_account_uid" &&
      $provenance == "Created" &&
      $use_label == "true" &&
      $managed_by_label == "azure-workload-identity-operator" &&
      $owner_uid_label == "$workload_identity_uid" &&
      $created_by_label == "true" &&
      $client_id_annotation == "$expected_client_id" &&
      ( -z $expected_tenant_id || $tenant_id_annotation == "$expected_tenant_id" ) ]]; then
      log VERIFY "WorkloadIdentity reconciliation preserved Created provenance, recorded the replacement UID, and repaired ServiceAccount metadata"
      return 0
    fi
    sleep_until_deadline "$deadline" 2 || true
  done

  log ERROR "Timed out waiting for ServiceAccount recreation and provenance reconciliation"
  oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o yaml >&2 || true
  oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o yaml >&2 || true
  return 1
}

wait_for_job_completion() {
  local name=$1
  local namespace=$2
  local timeout=$3
  local deadline
  local succeeded
  local failed
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while ((SECONDS < deadline)); do
    succeeded=$(oc get job "$name" -n "$namespace" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)
    failed=$(oc get job "$name" -n "$namespace" -o jsonpath='{.status.failed}' 2>/dev/null || true)
    if [[ ${succeeded:-0} =~ ^[0-9]+$ && ${succeeded:-0} -gt 0 ]]; then
      oc wait --for=condition=complete "job/$name" -n "$namespace" --timeout=30s >/dev/null || true
      return
    fi
    if [[ ${failed:-0} =~ ^[0-9]+$ && ${failed:-0} -gt 0 ]]; then
      log ERROR "Job/$name failed"
      return 1
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for Job/$name to complete"
  return 1
}

dump_job_diagnostics() {
  local name=$1
  local namespace=$2
  local pods
  local pod

  log READ "Describing Job/$name"
  oc describe "job/$name" -n "$namespace" >&2 || true

  pods=$(oc get pods -n "$namespace" -l "job-name=$name" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  if [[ -z $pods ]]; then
    log ERROR "No Pods found for Job/$name"
    return
  fi

  while IFS= read -r pod; do
    [[ -z $pod ]] && continue
    log READ "Describing Pod/$pod"
    oc describe pod "$pod" -n "$namespace" >&2 || true
    log READ "Printing Pod/$pod logs"
    oc logs pod/"$pod" -n "$namespace" --all-containers=true >&2 || true
    oc logs pod/"$pod" -n "$namespace" --all-containers=true --previous >&2 || true
  done <<<"$pods"
}

verify_workload_identity_azure_drift_recovery() {
  local deadline
  local credential_tuple
  local desired_subject
  local drift_issuer
  local drift_subject
  local expected_tuple

  if [[ $verify_workload_identity_azure_drift != "true" ]]; then
    return
  fi
  desired_subject="system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME"
  drift_issuer="https://drift.invalid/$WORKLOAD_IDENTITY_NAME"
  drift_subject="system:serviceaccount:$NAMESPACE:drifted-$SERVICE_ACCOUNT_NAME"
  expected_tuple=$issuer_url$'\n'$desired_subject$'\n'api://AzureADTokenExchange

  log UPDATE "Mutating Azure federated credential to verify periodic WorkloadIdentity drift recovery"
  if ! az identity federated-credential update \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
    --issuer "$drift_issuer" \
    --subject "$drift_subject" \
    --audiences api://AzureADTokenExchange \
    -o none; then
    log ERROR "Failed to mutate Azure federated credential $AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME"
    return 1
  fi

  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  log WATCH "Waiting for WorkloadIdentity periodic reconcile to restore Azure federated credential"
  while ((SECONDS < deadline)); do
    credential_tuple=$(az identity federated-credential show \
      --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
      --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
      --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
      --query '[issuer, subject, audiences[0]]' \
      -o tsv 2>/dev/null || true)
    if [[ $credential_tuple == "$expected_tuple" ]]; then
      log VERIFY "WorkloadIdentity periodic reconcile restored Azure federated credential"
      return
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for WorkloadIdentity periodic reconcile to restore Azure federated credential"
  az identity federated-credential show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
    -o json >&2 || true
  return 1
}

workload_identity_recovery_json() {
  local name=$1
  local current_uid=$2
  local previous_uid=$3

  cat <<EOF
{
  "apiVersion": "workloadidentity.azure.micosolutions.se/v1alpha1",
  "kind": "WorkloadIdentityRecovery",
  "metadata": {
    "name": "$name"
  },
  "spec": {
    "workloadIdentityRef": {
      "namespace": "$NAMESPACE",
      "name": "$WORKLOAD_IDENTITY_NAME",
      "uid": "$current_uid"
    },
    "previousWorkloadIdentityUid": "$previous_uid"
  }
}
EOF
}

assert_workload_identity_recovery_create_rejected() {
  local name=$1
  local current_uid=$2
  local previous_uid=$3
  local expected=$4
  local retry_until_rejected=${5:-false}
  local deadline
  local output
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while true; do
    if output=$(workload_identity_recovery_json "$name" "$current_uid" "$previous_uid" |
      oc create --dry-run=server -f - 2>&1); then
      if [[ $retry_until_rejected == "true" && $SECONDS -lt $deadline ]]; then
        log RETRY "Recovery admission cache has not observed the existing source UID yet; retrying"
        sleep_until_deadline "$deadline" 2 || true
        continue
      fi
      log ERROR "WorkloadIdentityRecovery/$name creation was not rejected"
      return 1
    fi
    if [[ $output == *"$expected"* ]]; then
      return
    fi
    log ERROR "Recovery creation failed for an unexpected reason: $output"
    return 1
  done
}

assert_workload_identity_recovery_create_allowed() {
  local name=$1
  local current_uid=$2
  local previous_uid=$3
  if ! workload_identity_recovery_json "$name" "$current_uid" "$previous_uid" |
    oc create --dry-run=server -f - >/dev/null; then
    log ERROR "Valid recovery creation was rejected by API-server admission"
    return 1
  fi
}

assert_workload_identity_recovery_delete_allowed() {
  local name=$1

  if ! oc delete workloadidentityrecovery "$name" --dry-run=server >/dev/null; then
    log ERROR "WorkloadIdentityRecovery/$name deletion admission was rejected"
    return 1
  fi
}

verify_workload_identity_controlled_recovery() {
  local recovery_name="$WORKLOAD_IDENTITY_NAME-recovery"
  local duplicate_recovery_name="$recovery_name-duplicate"
  local blocker_name="$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME-recovery-blocker"
  local invalid_uid=00000000-0000-0000-0000-000000000000
  local previous_uid
  local current_uid
  local reported_previous_uid
  local recovery_uid
  local tagged_uid
  local recovery_tag
  local recovery_target_tag
  local last_recovery_tag
  local credential_tuple
  local expected_tuple
  local service_account_owner
  local patch_output
  local recovery_plan
  local plan_identity_id
  local plan_issuer
  local plan_subject
  local plan_audience

  if [[ $verify_workload_identity_controlled_recovery != "true" ]]; then
    log SKIP "Skipping controlled WorkloadIdentity recovery verification"
    return
  fi
  controlled_recovery_started=true

  current_uid=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  log VERIFY "Verifying recovery cannot be created before WorkloadIdentity enters RecoveryRequired"
  assert_workload_identity_recovery_create_rejected \
    "$recovery_name-early" "$current_uid" "$invalid_uid" "RecoveryRequired" || return 1

  previous_uid=$current_uid
  log UPDATE "Changing WorkloadIdentity deletionPolicy to Retain for controlled recovery"
  oc patch workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --type=merge -p '{"spec":{"deletionPolicy":"Retain"}}' >/dev/null
  log DELETE "Deleting WorkloadIdentity/$WORKLOAD_IDENTITY_NAME while retaining its UAMI and ServiceAccount"
  oc delete workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --wait=true --timeout="$wait_timeout" >/dev/null

  tagged_uid=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-uid"' -o tsv)
  service_account_owner=$(oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" \
    -o go-template='{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}')
  if [[ $tagged_uid != "$previous_uid" || $service_account_owner != "$previous_uid" ]]; then
    log ERROR "Retained UAMI or ServiceAccount lost the previous WorkloadIdentity UID"
    return 1
  fi

  log APPLY "Recreating WorkloadIdentity/$WORKLOAD_IDENTITY_NAME with a new UID"
  render "$script_dir/workload-identity.yaml" | oc apply -f - >/dev/null
  current_uid=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  if [[ -z $current_uid || $current_uid == "$previous_uid" ]]; then
    log ERROR "Recreated WorkloadIdentity did not receive a new UID"
    return 1
  fi
  wait_for_workloadidentity_ready_false_reason "$WORKLOAD_IDENTITY_NAME" RecoveryRequired "$wait_timeout" || return 1
  reported_previous_uid=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    -o jsonpath='{.status.recovery.previousWorkloadIdentityUid}')
  if [[ $reported_previous_uid != "$previous_uid" ]]; then
    log ERROR "WorkloadIdentity recovery evidence is '$reported_previous_uid', want '$previous_uid'"
    return 1
  fi

  log VERIFY "Verifying recovery rejects the wrong current UID and source UID"
  assert_workload_identity_recovery_create_rejected \
    "$recovery_name-wrong-current" "$invalid_uid" "$previous_uid" "current WorkloadIdentity UID" || return 1
  assert_workload_identity_recovery_create_rejected \
    "$recovery_name-wrong-source" "$current_uid" "$invalid_uid" "requires source UID" || return 1

  log CREATE "Creating an extra FIC to verify recovery blocks before mutation"
  az identity federated-credential create \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$blocker_name" \
    --issuer "$issuer_url" \
    --subject "system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME-recovery-blocker" \
    --audiences api://AzureADTokenExchange \
    -o none
  created_recovery_blocker_fic=true

  assert_workload_identity_recovery_create_allowed "$recovery_name" "$current_uid" "$previous_uid" || return 1
  workload_identity_recovery_json "$recovery_name" "$current_uid" "$previous_uid" | oc create -f - >/dev/null
  applied_workload_identity_recovery=true

  log VERIFY "Verifying a second recovery for the same source UID is rejected"
  assert_workload_identity_recovery_create_rejected \
    "$duplicate_recovery_name" "$current_uid" "$previous_uid" "already exists" true || return 1

  if patch_output=$(oc patch workloadidentityrecovery "$recovery_name" --type=merge \
    -p "{\"spec\":{\"previousWorkloadIdentityUid\":\"$invalid_uid\"}}" 2>&1); then
    log ERROR "WorkloadIdentityRecovery/$recovery_name accepted a spec change"
    return 1
  fi
  if [[ $patch_output != *"spec is immutable"* ]]; then
    log ERROR "Recovery spec update failed for an unexpected reason: $patch_output"
    return 1
  fi

  log WATCH "Waiting for extra FIC to block recovery without mutation"
  oc wait --for=condition=Blocked workloadidentityrecovery/"$recovery_name" --timeout="$wait_timeout"
  assert_workload_identity_recovery_delete_allowed "$recovery_name" || return 1
  tagged_uid=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-uid"' -o tsv)
  recovery_tag=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-recovery-uid"' -o tsv)
  if [[ $tagged_uid != "$previous_uid" || -n $recovery_tag ]]; then
    log ERROR "Blocked recovery mutated UAMI ownership or fencing tags"
    return 1
  fi

  log DELETE "Removing extra FIC so the same recovery can resume"
  az identity federated-credential delete \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$blocker_name" \
    --yes -o none
  created_recovery_blocker_fic=false

  log WATCH "Waiting for WorkloadIdentityRecovery/$recovery_name to complete"
  oc wait --for=condition=Complete workloadidentityrecovery/"$recovery_name" --timeout="$wait_timeout"
  recovery_uid=$(oc get workloadidentityrecovery "$recovery_name" -o jsonpath='{.metadata.uid}')
  recovery_plan=$(oc get workloadidentityrecovery "$recovery_name" \
    -o go-template='{{.status.plan.userAssignedIdentity.id}}{{"\t"}}{{.status.plan.federatedIdentityCredential.issuer}}{{"\t"}}{{.status.plan.federatedIdentityCredential.subject}}{{"\t"}}{{index .status.plan.federatedIdentityCredential.audiences 0}}')
  IFS=$'\t' read -r plan_identity_id plan_issuer plan_subject plan_audience <<<"$recovery_plan"
  if [[ $(oc get workloadidentityrecovery "$recovery_name" -o jsonpath='{.status.mutationStarted}') != "true" ||
    $(oc get workloadidentityrecovery "$recovery_name" -o jsonpath='{.status.commitVerified}') != "true" ||
    -z $plan_identity_id ||
    $plan_issuer != "$issuer_url" ||
    $plan_subject != "system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME" ||
    $plan_audience != "api://AzureADTokenExchange" ]]; then
    log ERROR "Recovery completed without the expected forward-only plan and commit checkpoints"
    return 1
  fi

  log WATCH "Waiting for normal WorkloadIdentity reconciliation to resume"
  oc wait --for=condition=Ready workloadidentity/"$WORKLOAD_IDENTITY_NAME" \
    -n "$NAMESPACE" --timeout="$wait_timeout"
  tagged_uid=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-uid"' -o tsv)
  recovery_tag=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-recovery-uid"' -o tsv)
  recovery_target_tag=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-recovery-target-uid"' -o tsv)
  last_recovery_tag=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-last-recovery-uid"' -o tsv)
  if [[ $tagged_uid != "$current_uid" ||
    -n $recovery_tag ||
    -n $recovery_target_tag ||
    $last_recovery_tag != "$recovery_uid" ]]; then
    log ERROR "Committed UAMI recovery tags are incorrect"
    return 1
  fi

  expected_tuple=$issuer_url$'\n'"system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME"$'\n'api://AzureADTokenExchange
  credential_tuple=$(az identity federated-credential show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
    --query '[issuer, subject, audiences[0]]' -o tsv)
  service_account_owner=$(oc get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" \
    -o go-template='{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}')
  if [[ $credential_tuple != "$expected_tuple" || $service_account_owner != "$current_uid" ]]; then
    log ERROR "Recovered FIC tuple or ServiceAccount ownership is incorrect"
    return 1
  fi

  log RUN "Re-running Job/$JOB_NAME with the recovered workload identity"
  cleanup_kubernetes_resource "job/$JOB_NAME" "$NAMESPACE" || return 1
  render "$script_dir/job.yaml" | oc apply -f - >/dev/null
  if ! wait_for_job_completion "$JOB_NAME" "$NAMESPACE" "$wait_timeout"; then
    dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
    return 1
  fi
  if [[ $(oc logs "job/$JOB_NAME" -n "$NAMESPACE") != *"Successfully retrieved secret"* ]]; then
    log ERROR "Recovered workload identity Job did not report successful Key Vault access"
    dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
    return 1
  fi

  controlled_recovery_completed=true
  log DELETE "Deleting terminal WorkloadIdentityRecovery record"
  oc delete workloadidentityrecovery "$recovery_name" \
    --wait=true --timeout="$wait_timeout" >/dev/null
  applied_workload_identity_recovery=false
  log VERIFY "Real admission rejected the duplicate source; forward-only controlled recovery resumed from Blocked and completed"
}

render() {
  local file=$1
  local content
  content=$(<"$file")
  local vars=(
    AZURE_STORAGE_ACCOUNT_NAME AZURE_BLOB_CONTAINER_NAME
    SIGNING_KEY_SECRET_NAMESPACE SIGNING_KEY_SECRET_NAME SIGNING_KEY_SECRET_KEY OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER OIDC_ISSUER_DELETION_POLICY
    NAMESPACE WORKLOAD_IDENTITY_NAME SERVICE_ACCOUNT_NAME AZURE_USER_ASSIGNED_IDENTITY_NAME AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME WORKLOAD_IDENTITY_DELETION_POLICY
    IMAGE_NAME JOB_NAME KEY_VAULT_NAME KEY_VAULT_SECRET_NAME KEY_VAULT_READ_TIMEOUT_SECONDS
  )
  local var
  for var in "${vars[@]}"; do
    content=${content//\$\{$var\}/${!var}}
  done
  printf '%s\n' "$content"
}

tmpdir=$(mktemp -d)
chmod 700 "$tmpdir"

begin_step 1
prepare_operator_credentials
if [[ -n $operator_candidate_run_id ]]; then
  prepare_release_candidate
elif [[ -n $operator_image_repository || -n $operator_image_digest || -n $operator_candidate_commit ]]; then
  die "OPERATOR_CANDIDATE_RUN_ID is required when candidate metadata overrides are set"
fi
assert_shared_resource_group_absent
assert_key_vault_resource_group_absent
install_cert_manager_dependency

begin_step 2
if [[ -n $operator_candidate_run_id ]]; then
  ensure_operator_namespace
  log SKIP "Using release candidate commit $operator_candidate_commit and image $operator_image_repository@$operator_image_digest"
else
  build_operator_image
fi

begin_step 3
install_operator_release

begin_step 4
verify_operator_release

begin_step 5
if ! oc get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log CREATE "Creating test namespace $NAMESPACE"
  oc create namespace "$NAMESPACE"
  created_test_namespace=true
fi

begin_step 6
capture_original_openshift_service_account_issuer
log APPLY "Applying OIDCIssuer/default"
render "$script_dir/oidc-issuer.yaml" | oc apply -f -
applied_oidc_issuer=true
wait_for_shared_resource_group_created

begin_step 7
if [[ $assign_oidc_storage_blob_role == "true" ]]; then
  storage_account_id=$(wait_for_storage_account_id)
  ensure_role_assignment "$operator_principal_id" ServicePrincipal "$oidc_storage_blob_role" "$storage_account_id"
fi

begin_step 8
log WATCH "Waiting for OIDCIssuer/default to become Ready"
oc wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout"
issuer_url=$(oc get oidcissuer default -o jsonpath='{.status.issuerURL}')
if [[ -z $issuer_url ]]; then
  die "OIDCIssuer default is missing status.issuerURL"
fi

begin_step 9
verify_oidcissuer_captured_previous_service_account_issuer "$issuer_url"

begin_step 10
verify_signing_key_rotation_publish "$issuer_url"

begin_step 11
wait_for_openshift_api_server_rollout "$issuer_url"

begin_step 12
verify_oidc_issuer_periodic_refresh_publish "$issuer_url"

begin_step 13
ensure_key_vault_exists

begin_step 14
log APPLY "Applying WorkloadIdentity/$WORKLOAD_IDENTITY_NAME"
render "$script_dir/workload-identity.yaml" | oc apply -f -
applied_workload_identity=true
log WATCH "Waiting for WorkloadIdentity/$WORKLOAD_IDENTITY_NAME to become Ready"
oc wait --for=condition=Ready "workloadidentity/$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"
verify_workload_identity_immutable_name_and_tags
verify_workload_identity_service_account_recreation

begin_step 15
verify_workload_identity_conflict_reconciliation
log DELETE "Deleting conflict WorkloadIdentity test resources before continuing"
cleanup_conflict_workload_identity
cleanup_conflict_service_account

begin_step 16
if [[ -z $vault_id ]]; then
  if ! vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv 2>/dev/null); then
    die "Key Vault $KEY_VAULT_NAME was not found; create it first or set KEY_VAULT_NAME to an existing vault"
  fi
fi
if [[ -z $vault_id ]]; then
  die "Key Vault $KEY_VAULT_NAME returned an empty resource ID"
fi

if [[ $upload_key_vault_secret == "true" ]]; then
  if [[ $assign_key_vault_secret_writer_role == "true" ]]; then
    resolve_active_azure_principal
    ensure_role_assignment "$active_azure_principal_id" "$active_azure_principal_type" "$key_vault_secret_writer_role" "$vault_id"
  fi
  upload_key_vault_secret_with_retry
fi

principal_id=$(oc get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.status.principalID}')
if [[ -z $principal_id ]]; then
  die "WorkloadIdentity $NAMESPACE/$WORKLOAD_IDENTITY_NAME is missing status.principalID"
fi

if [[ $assign_role == "true" ]]; then
  ensure_role_assignment "$principal_id" ServicePrincipal "$key_vault_role" "$vault_id"
fi

begin_step 17
wait_for_schedulable_node "$wait_timeout"

reader_dir="$script_dir/keyvault-secret-reader"
reader_build_context="$tmpdir/keyvault-secret-reader-build"
prepare_key_vault_secret_reader_build_context "$reader_dir" "$reader_build_context"

if ! oc get buildconfig "$IMAGE_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  log CREATE "Creating OpenShift BuildConfig/$IMAGE_NAME"
  oc new-build --name="$IMAGE_NAME" --binary --strategy=docker -n "$NAMESPACE" >/dev/null
  created_buildconfig=true
fi
log BUILD "Building Key Vault secret reader image $IMAGE_NAME"
if ! oc start-build "$IMAGE_NAME" --from-dir="$reader_build_context" --follow --wait -n "$NAMESPACE"; then
  oc get builds,pods -n "$NAMESPACE" >&2 || true
  exit 1
fi
verify_latest_openshift_build_completed "$IMAGE_NAME" "$NAMESPACE" || exit 1

wait_for_schedulable_node "$wait_timeout"

if oc get job "$JOB_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  log DELETE "Deleting previous Job/$JOB_NAME"
  cleanup_kubernetes_resource "job/$JOB_NAME" "$NAMESPACE"
fi
log APPLY "Applying Job/$JOB_NAME"
render "$script_dir/job.yaml" | oc apply -f -
applied_job=true

begin_step 18
log WATCH "Waiting for Job/$JOB_NAME to complete"
if ! wait_for_job_completion "$JOB_NAME" "$NAMESPACE" "$wait_timeout"; then
  dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
  exit 1
fi
log READ "Printing Job/$JOB_NAME logs"
oc logs "job/$JOB_NAME" -n "$NAMESPACE"

begin_step 19
verify_workload_identity_azure_drift_recovery

begin_step 20
verify_workload_identity_controlled_recovery

begin_step 21
assert_oidcissuer_delete_rejected_by_workload_identity
