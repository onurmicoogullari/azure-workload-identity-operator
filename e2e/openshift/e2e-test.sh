#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
OpenShift e2e smoke test for OIDCIssuer + WorkloadIdentity + Azure Key Vault.

Uses the current kubeconfig/oc session and active Azure CLI account.

Optional env:
  AZURE_SUBSCRIPTION_ID                       default: current az account
  AZURE_TENANT_ID                             default: current az account tenant
  AZURE_LOCATION                              default: swedencentral
  INSTALL_AZURE_WORKLOAD_IDENTITY_WEBHOOK     default: true
  AZURE_WORKLOAD_IDENTITY_WEBHOOK_NAMESPACE   default: azure-workload-identity-system
  AZURE_WORKLOAD_IDENTITY_WEBHOOK_RELEASE     default: workload-identity-webhook
  AZURE_WORKLOAD_IDENTITY_HELM_REPO_NAME      default: azure-workload-identity
  AZURE_WORKLOAD_IDENTITY_HELM_REPO_URL       default: https://azure.github.io/azure-workload-identity/charts
  AZURE_WORKLOAD_IDENTITY_HELM_CHART          default: AZURE_WORKLOAD_IDENTITY_HELM_REPO_NAME/workload-identity-webhook
  AZURE_WORKLOAD_IDENTITY_WEBHOOK_REPLICA_COUNT default: 1
  AZURE_WORKLOAD_IDENTITY_OPENSHIFT_COMPATIBILITY default: true
  CLEANUP_INCOMPLETE_WEBHOOK_HELM_RELEASE     default: true
  INSTALL_OPERATOR_CRDS                       default: true
  RUN_OPERATOR_LOCALLY                        default: true (TODO: switch to false after in-cluster deploy support is added)
  OPERATOR_READY_TIMEOUT                      default: WAIT_TIMEOUT
  OPERATOR_HEALTH_PROBE_BIND_ADDRESS          default: first available localhost port
  OPERATOR_LOG_FILE                           default: temp file
  OPERATOR_WEBHOOK_URL                        default: https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-oidcissuer
                                                local mode uses this to test webhook handler logic, not API server admission integration
  ENSURE_KEY_VAULT                            default: true
  ENABLE_KEY_VAULT_RBAC                       default: true
  AZURE_RESOURCE_GROUP_NAME                   default: rg-azwi-crc-storage-test (OIDCIssuer-owned group)
  AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME default: rg-azwi-crc-wi-test
  AZURE_KEY_VAULT_RESOURCE_GROUP_NAME         default: rg-azwi-crc-kv-test
  REQUIRE_OPERATOR_CREATED_OIDC_RESOURCE_GROUP default: VERIFY_OIDC_RESOURCE_GROUP_DELETED
  REQUIRE_OPERATOR_CREATED_WORKLOAD_IDENTITY_RESOURCE_GROUP default: VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED
  VERIFY_OIDC_RESOURCE_GROUP_DELETED          default: ENSURE_KEY_VAULT
  VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED default: true
  AZURE_STORAGE_ACCOUNT_NAME                  default: stazwicrctest
  AZURE_BLOB_CONTAINER_NAME                   default: oidc
  ASSIGN_OIDC_STORAGE_BLOB_ROLE               default: true
  OIDC_STORAGE_BLOB_ROLE                      default: Storage Blob Data Contributor
  OPERATOR_AZURE_PRINCIPAL_ID                 default: active Azure CLI principal object ID
  OPERATOR_AZURE_PRINCIPAL_TYPE               default: inferred from active Azure CLI account, or ServicePrincipal when OPERATOR_AZURE_PRINCIPAL_ID is set
  AZURE_USER_ASSIGNED_IDENTITY_NAME           default: id-azwi-crc-test
  AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME    default: fidc-azwi-crc-test
  KEY_VAULT_NAME                              default: kv-azwi-<HHMMSS>-<random>
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
  RETIRING_SIGNING_KEY_SECRET_NAMESPACE       default: NAMESPACE
  RETIRING_SIGNING_KEY_SECRET_NAME            default: azwi-crc-retiring-signing-key
  RETIRING_SIGNING_KEY_SECRET_KEY             default: service-account.pub
  OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER     default: true
  OIDC_ISSUER_DELETION_POLICY                 default: Delete
  WORKLOAD_IDENTITY_DELETION_POLICY           default: Delete
  VERIFY_WORKLOAD_IDENTITY_CONFLICTS          default: true
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
  ./e2e/openshift/e2e-test.sh

The OIDCIssuer storage, WorkloadIdentity resources, and test Key Vault use
separate Azure resource groups so each cleanup path is verified independently.
EOF
}

if [[ ${1:-} == "-h" || ${1:-} == "--help" ]]; then
  usage
  exit 0
fi

log() {
  local operation=${1:-INFO}
  local message
  local prefix
  if (($# > 0)); then
    shift
  fi
  message=${*:-}
  prefix=$(log_prefix "$operation")

  while IFS= read -r line; do
    printf '%b %s\n' "$prefix" "$line" >&2
  done <<<"$message"
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
  [[ -t 2 && ${TERM:-} != "dumb" ]]
}

indent_output() {
  while IFS= read -r line; do
    printf '         %s\n' "$line"
  done
}

exec > >(indent_output)

die() {
  primary_failure=$*
  log ERROR "$*"
  exit 1
}

need() {
  command -v "$1" >/dev/null || die "missing required command: $1"
}

local_port_in_use() {
  local port=$1
  (: <"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1
}

available_local_bind_address() {
  local port
  for _ in {1..100}; do
    port=$((20000 + RANDOM))
    if ! local_port_in_use "$port"; then
      printf '127.0.0.1:%s\n' "$port"
      return
    fi
  done

  log ERROR "Could not find an available local health probe port"
  return 1
}

need kubectl
need oc
need az

if [[ -z ${AZURE_SUBSCRIPTION_ID:-} ]]; then
  AZURE_SUBSCRIPTION_ID=$(az account show --query id -o tsv)
fi
if [[ -z ${AZURE_TENANT_ID:-} ]]; then
  AZURE_TENANT_ID=$(az account show --query tenantId -o tsv)
fi
AZURE_LOCATION=${AZURE_LOCATION:-swedencentral}
AZURE_RESOURCE_GROUP_NAME=${AZURE_RESOURCE_GROUP_NAME:-rg-azwi-crc-storage-test}
AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME=${AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME:-rg-azwi-crc-wi-test}
AZURE_KEY_VAULT_RESOURCE_GROUP_NAME=${AZURE_KEY_VAULT_RESOURCE_GROUP_NAME:-rg-azwi-crc-kv-test}
export AZURE_SUBSCRIPTION_ID
export AZURE_TENANT_ID
export AZURE_LOCATION
export AZURE_RESOURCE_GROUP_NAME
export AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME
export AZURE_KEY_VAULT_RESOURCE_GROUP_NAME
install_azure_workload_identity_webhook=${INSTALL_AZURE_WORKLOAD_IDENTITY_WEBHOOK:-true}
azure_workload_identity_webhook_namespace=${AZURE_WORKLOAD_IDENTITY_WEBHOOK_NAMESPACE:-azure-workload-identity-system}
azure_workload_identity_webhook_release=${AZURE_WORKLOAD_IDENTITY_WEBHOOK_RELEASE:-workload-identity-webhook}
azure_workload_identity_helm_repo_name=${AZURE_WORKLOAD_IDENTITY_HELM_REPO_NAME:-azure-workload-identity}
azure_workload_identity_helm_repo_url=${AZURE_WORKLOAD_IDENTITY_HELM_REPO_URL:-https://azure.github.io/azure-workload-identity/charts}
azure_workload_identity_helm_chart=${AZURE_WORKLOAD_IDENTITY_HELM_CHART:-$azure_workload_identity_helm_repo_name/workload-identity-webhook}
azure_workload_identity_webhook_replica_count=${AZURE_WORKLOAD_IDENTITY_WEBHOOK_REPLICA_COUNT:-1}
azure_workload_identity_openshift_compatibility=${AZURE_WORKLOAD_IDENTITY_OPENSHIFT_COMPATIBILITY:-true}
cleanup_incomplete_webhook_helm_release=${CLEANUP_INCOMPLETE_WEBHOOK_HELM_RELEASE:-true}
install_operator_crds=${INSTALL_OPERATOR_CRDS:-true}
run_operator_locally=${RUN_OPERATOR_LOCALLY:-true} # TODO: default to false after this script deploys the operator in-cluster.
operator_ready_timeout=${OPERATOR_READY_TIMEOUT:-${WAIT_TIMEOUT:-10m}}
if [[ -n ${OPERATOR_HEALTH_PROBE_BIND_ADDRESS:-} ]]; then
  operator_health_probe_bind_address=$OPERATOR_HEALTH_PROBE_BIND_ADDRESS
else
  if ! operator_health_probe_bind_address=$(available_local_bind_address); then
    exit 1
  fi
fi
operator_log_file=${OPERATOR_LOG_FILE:-}
operator_webhook_url=${OPERATOR_WEBHOOK_URL:-https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-oidcissuer}
ensure_key_vault=${ENSURE_KEY_VAULT:-true}
enable_key_vault_rbac=${ENABLE_KEY_VAULT_RBAC:-true}
verify_oidc_resource_group_deleted=${VERIFY_OIDC_RESOURCE_GROUP_DELETED:-$ensure_key_vault}
require_operator_created_oidc_resource_group=${REQUIRE_OPERATOR_CREATED_OIDC_RESOURCE_GROUP:-$verify_oidc_resource_group_deleted}
verify_workload_identity_resource_group_deleted=${VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED:-true}
require_operator_created_workload_identity_resource_group=${REQUIRE_OPERATOR_CREATED_WORKLOAD_IDENTITY_RESOURCE_GROUP:-$verify_workload_identity_resource_group_deleted}
AZURE_STORAGE_ACCOUNT_NAME=${AZURE_STORAGE_ACCOUNT_NAME:-stazwicrctest}
AZURE_BLOB_CONTAINER_NAME=${AZURE_BLOB_CONTAINER_NAME:-oidc}
export AZURE_STORAGE_ACCOUNT_NAME
export AZURE_BLOB_CONTAINER_NAME
assign_oidc_storage_blob_role=${ASSIGN_OIDC_STORAGE_BLOB_ROLE:-true}
oidc_storage_blob_role=${OIDC_STORAGE_BLOB_ROLE:-Storage Blob Data Contributor}
AZURE_USER_ASSIGNED_IDENTITY_NAME=${AZURE_USER_ASSIGNED_IDENTITY_NAME:-id-azwi-crc-test}
AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME=${AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME:-fidc-azwi-crc-test}
if [[ -z ${KEY_VAULT_NAME:-} ]]; then
  KEY_VAULT_NAME="kv-azwi-$(date -u +%H%M%S)-$(printf '%04d' $((RANDOM % 10000)))"
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
NAMESPACE=${NAMESPACE:-azwi-crc-test}
WORKLOAD_IDENTITY_NAME=${WORKLOAD_IDENTITY_NAME:-azwi-crc-test}
SERVICE_ACCOUNT_NAME=${SERVICE_ACCOUNT_NAME:-$WORKLOAD_IDENTITY_NAME}
SIGNING_KEY_SECRET_NAMESPACE=${SIGNING_KEY_SECRET_NAMESPACE:-openshift-kube-apiserver}
SIGNING_KEY_SECRET_NAME=${SIGNING_KEY_SECRET_NAME:-bound-service-account-signing-key}
SIGNING_KEY_SECRET_KEY=${SIGNING_KEY_SECRET_KEY:-service-account.pub}
verify_signing_key_rotation=${VERIFY_SIGNING_KEY_ROTATION:-true}
RETIRING_SIGNING_KEY_SECRET_NAMESPACE=${RETIRING_SIGNING_KEY_SECRET_NAMESPACE:-$NAMESPACE}
RETIRING_SIGNING_KEY_SECRET_NAME=${RETIRING_SIGNING_KEY_SECRET_NAME:-azwi-crc-retiring-signing-key}
RETIRING_SIGNING_KEY_SECRET_KEY=${RETIRING_SIGNING_KEY_SECRET_KEY:-service-account.pub}
OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER=${OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER:-true}
OIDC_ISSUER_DELETION_POLICY=${OIDC_ISSUER_DELETION_POLICY:-Delete}
WORKLOAD_IDENTITY_DELETION_POLICY=${WORKLOAD_IDENTITY_DELETION_POLICY:-Delete}
verify_workload_identity_conflicts=${VERIFY_WORKLOAD_IDENTITY_CONFLICTS:-true}
IMAGE_NAME=${IMAGE_NAME:-azwi-crc-test}
JOB_NAME=${JOB_NAME:-azwi-crc-test}
KEY_VAULT_READ_TIMEOUT_SECONDS=${KEY_VAULT_READ_TIMEOUT_SECONDS:-300}
export NAMESPACE
export WORKLOAD_IDENTITY_NAME
export SERVICE_ACCOUNT_NAME
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

az account set --subscription "$AZURE_SUBSCRIPTION_ID"

wait_timeout=${WAIT_TIMEOUT:-10m}
azure_rbac_propagation_timeout=${AZURE_RBAC_PROPAGATION_TIMEOUT:-5m}
key_vault_purge_timeout=${KEY_VAULT_PURGE_TIMEOUT:-20m}
assign_role=${ASSIGN_KEYVAULT_ROLE:-true}
key_vault_role=${KEY_VAULT_ROLE:-Key Vault Secrets User}

if [[ $install_azure_workload_identity_webhook == "true" ]]; then
  need helm
fi
if [[ $install_operator_crds == "true" || $run_operator_locally == "true" ]]; then
  need make
  need go
fi
if [[ $run_operator_locally == "true" ]]; then
  need curl
  need openssl
fi
if [[ $verify_signing_key_rotation == "true" ]]; then
  need curl
  need openssl
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
tmpdir=""
operator_pid=""
operator_webhook_cert_dir=""
vault_id=""
# Cleanup ownership:
# - script-created resources are tracked here and deleted by this script
# - operator-created Azure resources are deleted only by deleting their CRs and waiting for finalizers
applied_oidc_issuer=false
applied_workload_identity=false
created_buildconfig=false
applied_job=false
applied_conflict_workload_identity=false
applied_federated_credential_conflict_workload_identity=false
created_conflict_service_account=false
oidc_deleted=false
workload_identity_deleted=false
original_openshift_service_account_issuer=""
# The original issuer can be an empty string, so track whether capture actually ran.
captured_original_openshift_service_account_issuer=false
created_workload_identity_webhook_namespace=false
created_workload_identity_webhook_release=false
created_test_namespace=false
created_retiring_signing_key_secret=false
active_azure_principal_id=""
active_azure_principal_type=""
primary_failure=""
cleanup_failed=false
created_key_vault_resource_group=false
created_role_assignment_ids=()

cleanup_job() {
  if [[ $applied_job == "true" ]]; then
    kubectl delete job "$JOB_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete "job/$JOB_NAME" -n "$NAMESPACE" --timeout="$wait_timeout" >/dev/null || return 1
  fi
}

cleanup_conflict_workload_identity() {
  if [[ $applied_conflict_workload_identity == "true" ]]; then
    kubectl delete workloadidentity azwi-sa-conflict -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete workloadidentity/azwi-sa-conflict -n "$NAMESPACE" --timeout="$wait_timeout" >/dev/null || return 1
  fi
  if [[ $applied_federated_credential_conflict_workload_identity == "true" ]]; then
    kubectl delete workloadidentity azwi-fic-conflict -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete workloadidentity/azwi-fic-conflict -n "$NAMESPACE" --timeout="$wait_timeout" >/dev/null || return 1
  fi
}

cleanup_conflict_service_account() {
  if [[ $created_conflict_service_account == "true" ]]; then
    kubectl delete serviceaccount azwi-sa-conflict -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete serviceaccount/azwi-sa-conflict -n "$NAMESPACE" --timeout="$wait_timeout" >/dev/null || return 1
  fi
}

cleanup_workload_identity() {
  workload_identity_deleted=false
  if [[ $applied_workload_identity == "true" ]]; then
    if [[ $verify_workload_identity_resource_group_deleted == "true" && $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" && $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME != "$AZURE_RESOURCE_GROUP_NAME" ]]; then
      log WATCH "Deleting WorkloadIdentity/$WORKLOAD_IDENTITY_NAME and waiting for its finalizer to delete Azure resource group $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME"
    fi
    kubectl delete workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1
    if kubectl wait --for=delete "workloadidentity/$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --timeout="$wait_timeout" >/dev/null 2>&1; then
      workload_identity_deleted=true
    else
      return 1
    fi
  fi
}

verify_workload_identity_resource_group_cleanup() {
  if [[ $workload_identity_deleted == "true" && $verify_workload_identity_resource_group_deleted == "true" && $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" ]]; then
    if [[ $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME == "$AZURE_RESOURCE_GROUP_NAME" ]]; then
      log SKIP "Skipping separate WorkloadIdentity resource group deletion verification because it matches the OIDCIssuer resource group"
    else
      wait_for_azure_resource_group_deleted "$AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME" "$wait_timeout" "Operator deleted WorkloadIdentity Azure resource group"
    fi
  fi
}

cleanup_oidc_issuer() {
  local verify_openshift_handoff_guard=${1:-false}
  oidc_deleted=false
  if [[ $applied_oidc_issuer == "true" ]]; then
    if [[ $verify_openshift_handoff_guard == "true" ]]; then
      assert_oidcissuer_delete_rejected_by_openshift_service_account_issuer || return 1
    fi
    handoff_openshift_service_account_issuer_before_oidcissuer_delete || return 1
    if [[ $verify_oidc_resource_group_deleted == "true" && $OIDC_ISSUER_DELETION_POLICY == "Delete" ]]; then
      log WATCH "Deleting OIDCIssuer/default and waiting for its finalizer to delete Azure resource group $AZURE_RESOURCE_GROUP_NAME"
    fi
    kubectl delete oidcissuer default --ignore-not-found --wait=false >/dev/null 2>&1
    if kubectl wait --for=delete oidcissuer/default --timeout="$wait_timeout" >/dev/null 2>&1; then
      oidc_deleted=true
    else
      return 1
    fi
  fi
}

verify_oidc_resource_group_cleanup() {
  if [[ $oidc_deleted == "true" && $verify_oidc_resource_group_deleted == "true" && $OIDC_ISSUER_DELETION_POLICY == "Delete" ]]; then
    wait_for_azure_resource_group_deleted "$AZURE_RESOURCE_GROUP_NAME" "$wait_timeout" "Operator deleted OIDCIssuer Azure resource group"
  fi
}

cleanup_build_artifacts() {
  if [[ $created_buildconfig == "true" ]]; then
    oc delete buildconfig "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    oc delete imagestream "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    wait_for_kubernetes_resource_absent "buildconfig/$IMAGE_NAME" "$NAMESPACE" || return 1
    wait_for_kubernetes_resource_absent "imagestream/$IMAGE_NAME" "$NAMESPACE" || return 1
  fi
}

stop_local_operator() {
  local deadline
  if [[ -n $operator_pid ]]; then
    pkill -TERM -P "$operator_pid" >/dev/null 2>&1 || true
    kill "$operator_pid" >/dev/null 2>&1 || true
    deadline=$((SECONDS + 30))
    while (( SECONDS < deadline )); do
      if ! kill -0 "$operator_pid" >/dev/null 2>&1; then
        wait "$operator_pid" >/dev/null 2>&1 || true
        return 0
      fi
      sleep 1
    done
    pkill -KILL -P "$operator_pid" >/dev/null 2>&1 || true
    kill -KILL "$operator_pid" >/dev/null 2>&1 || true
    wait "$operator_pid" >/dev/null 2>&1 || true
    if ! kill -0 "$operator_pid" >/dev/null 2>&1; then
      return 0
    fi
    log ERROR "Local operator process $operator_pid did not stop"
    return 1
  fi
  return 0
}

cleanup_workload_identity_webhook_release() {
  if [[ $created_workload_identity_webhook_release == "true" ]]; then
    if helm status "$azure_workload_identity_webhook_release" -n "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
      log DELETE "Uninstalling Azure Workload Identity webhook Helm release $azure_workload_identity_webhook_release"
      helm uninstall "$azure_workload_identity_webhook_release" \
        -n "$azure_workload_identity_webhook_namespace" \
        --wait \
        --timeout "$wait_timeout" >/dev/null || return 1
      if helm status "$azure_workload_identity_webhook_release" -n "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
        log ERROR "Helm release $azure_workload_identity_webhook_release still exists"
        return 1
      fi
    fi
  fi
}

cleanup_test_namespace() {
  if [[ $created_test_namespace == "true" ]]; then
    log DELETE "Deleting test namespace $NAMESPACE created by e2e test"
    kubectl delete namespace "$NAMESPACE" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete "namespace/$NAMESPACE" --timeout="$wait_timeout" >/dev/null || return 1
  fi
}

cleanup_retiring_signing_key_secret() {
  if [[ $created_retiring_signing_key_secret == "true" ]]; then
    kubectl delete secret "$RETIRING_SIGNING_KEY_SECRET_NAME" -n "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" --ignore-not-found >/dev/null || return 1
  fi
}

cleanup_workload_identity_webhook_namespace() {
  if [[ $created_workload_identity_webhook_namespace == "true" ]]; then
    log DELETE "Deleting Azure Workload Identity webhook namespace $azure_workload_identity_webhook_namespace created by e2e test"
    kubectl delete namespace "$azure_workload_identity_webhook_namespace" --ignore-not-found --wait=false >/dev/null || return 1
    kubectl wait --for=delete "namespace/$azure_workload_identity_webhook_namespace" --timeout="$wait_timeout" >/dev/null || return 1
  fi
}

cleanup_tmpdir() {
  if [[ -z $tmpdir ]]; then
    return 0
  fi
  rm -rf "$tmpdir"
}

cleanup_key_vault_resource_group() {
  if [[ -n $vault_id ]] || az keyvault show -n "$KEY_VAULT_NAME" -g "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log DELETE "Deleting Key Vault $KEY_VAULT_NAME"
    az keyvault delete -n "$KEY_VAULT_NAME" -g "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" -o none >/dev/null || return 1
  fi
  purge_deleted_key_vault false

  if [[ $created_key_vault_resource_group == "true" ]]; then
    log DELETE "Deleting Key Vault resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
    az group delete -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" --yes --no-wait >/dev/null || return 1
    wait_for_azure_resource_group_deleted "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" "$wait_timeout" "Script deleted Key Vault Azure resource group"
  fi

  return 0
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
  local namespace=$2
  local deadline
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while (( SECONDS < deadline )); do
    if ! kubectl get "$resource" -n "$namespace" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done

  log ERROR "Kubernetes resource still exists: $namespace/$resource"
  return 1
}

wait_for_role_assignment_deleted() {
  local assignment_id=$1
  local deadline
  local existing_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while (( SECONDS < deadline )); do
    existing_id=$(az role assignment list --all --query "[?id=='$assignment_id'].id | [0]" -o tsv 2>/dev/null || true)
    if [[ -z $existing_id ]]; then
      return 0
    fi
    sleep 5
  done

  log ERROR "Role assignment still exists: $assignment_id"
  return 1
}

purge_deleted_key_vault() {
  local wait_for_purge=${1:-true}
  local deadline
  local deleted_id
  local location
  deadline=$((SECONDS + $(duration_seconds "$key_vault_purge_timeout")))

  deleted_id=$(az keyvault show-deleted --name "$KEY_VAULT_NAME" --query id -o tsv 2>/dev/null || true)
  if [[ -z $deleted_id ]]; then
    return 0
  fi

  location=$(az keyvault show-deleted --name "$KEY_VAULT_NAME" --query properties.location -o tsv 2>/dev/null || true)
  location=${location:-$AZURE_LOCATION}
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
    if ! az keyvault show-deleted --name "$KEY_VAULT_NAME" --query id -o tsv >/dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done

  if ! az keyvault show-deleted --name "$KEY_VAULT_NAME" --query id -o tsv >/dev/null 2>&1; then
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
  local verify_openshift_handoff_guard=false
  set +e

  [[ $initial_exit_code -eq 0 ]] && verify_openshift_handoff_guard=true

  log CLEANUP "Cleaning up e2e resources"

  cleanup_step "delete Job" cleanup_job
  cleanup_step "delete conflict WorkloadIdentity CR" cleanup_conflict_workload_identity
  cleanup_step "delete conflict ServiceAccount" cleanup_conflict_service_account
  cleanup_step "delete WorkloadIdentity CR" cleanup_workload_identity
  cleanup_step "verify WorkloadIdentity Azure cleanup" verify_workload_identity_resource_group_cleanup
  cleanup_step "delete OIDCIssuer CR" cleanup_oidc_issuer "$verify_openshift_handoff_guard"
  cleanup_step "verify OIDCIssuer Azure cleanup" verify_oidc_resource_group_cleanup
  cleanup_step "delete script-created role assignments" cleanup_role_assignments
  cleanup_step "delete OpenShift build artifacts" cleanup_build_artifacts
  cleanup_step "delete Key Vault resources" cleanup_key_vault_resource_group
  cleanup_step "delete retiring signing key Secret" cleanup_retiring_signing_key_secret
  cleanup_step "stop local operator" stop_local_operator
  cleanup_step "uninstall Azure Workload Identity webhook" cleanup_workload_identity_webhook_release
  cleanup_step "delete test namespace" cleanup_test_namespace
  cleanup_step "delete Azure Workload Identity webhook namespace" cleanup_workload_identity_webhook_namespace
  cleanup_step "delete temporary directory" cleanup_tmpdir

  if [[ $initial_exit_code -eq 0 && $cleanup_failed != "true" ]]; then
    final_result_operation=PASS
    final_result_message="OpenShift e2e test passed"
  else
    if [[ -n $primary_failure ]]; then
      final_result_message="OpenShift e2e test failed: $primary_failure"
    elif [[ $initial_exit_code -ne 0 ]]; then
      final_result_message="OpenShift e2e test failed before cleanup completed"
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
  if [[ -n $active_azure_principal_id ]]; then
    return
  fi

  local account_type
  local principal_name
  account_type=$(az account show --query user.type -o tsv)
  principal_name=$(az account show --query user.name -o tsv)

  case "$account_type" in
    user)
      if ! active_azure_principal_id=$(az ad signed-in-user show --query id -o tsv 2>/dev/null); then
        die "Could not infer the active Azure CLI user object ID; set OPERATOR_AZURE_PRINCIPAL_ID explicitly"
      fi
      active_azure_principal_type=User
      ;;
    servicePrincipal)
      if ! active_azure_principal_id=$(az ad sp show --id "$principal_name" --query id -o tsv 2>/dev/null); then
        die "Could not infer the active Azure CLI service principal object ID; set OPERATOR_AZURE_PRINCIPAL_ID explicitly"
      fi
      active_azure_principal_type=ServicePrincipal
      ;;
    *)
      die "Could not infer Azure principal type '$account_type'; set OPERATOR_AZURE_PRINCIPAL_ID explicitly"
      ;;
  esac

  if [[ -z $active_azure_principal_id ]]; then
    die "Could not infer the active Azure CLI principal object ID; set OPERATOR_AZURE_PRINCIPAL_ID explicitly"
  fi
}

ensure_role_assignment() {
  local principal_id=$1
  local principal_type=$2
  local role=$3
  local scope=$4
  local created_id
  local existing_id

  existing_id=$(az role assignment list --assignee "$principal_id" --role "$role" --scope "$scope" --query '[0].id' -o tsv)
  if [[ -n $existing_id ]]; then
    log SKIP "Role assignment already exists: role '$role' on '$scope'"
    return
  fi

  log CREATE "Creating role assignment: role '$role' on '$scope'"
  created_id=$(az role assignment create \
    --assignee-object-id "$principal_id" \
    --assignee-principal-type "$principal_type" \
    --role "$role" \
    --scope "$scope" \
    --query id \
    -o tsv)
  created_role_assignment_ids+=("$created_id")
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
  kubectl describe oidcissuer default >&2 || true
  exit 1
}

assert_oidc_resource_group_absent() {
  if [[ $require_operator_created_oidc_resource_group != "true" ]]; then
    return
  fi

  if az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log ERROR "Resource group $AZURE_RESOURCE_GROUP_NAME already exists.
This e2e test is configured to verify OIDCIssuer-created resource group deletion, so the OIDCIssuer must create the group itself.
Delete the resource group, choose another AZURE_RESOURCE_GROUP_NAME, or set REQUIRE_OPERATOR_CREATED_OIDC_RESOURCE_GROUP=false and VERIFY_OIDC_RESOURCE_GROUP_DELETED=false."
    exit 1
  fi
}

assert_workload_identity_resource_group_absent() {
  if [[ $require_operator_created_workload_identity_resource_group != "true" ]]; then
    return
  fi
  if [[ $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME == "$AZURE_RESOURCE_GROUP_NAME" ]]; then
    return
  fi

  if az group show -n "$AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log ERROR "Resource group $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME already exists.
This e2e test is configured to verify WorkloadIdentity-created resource group deletion, so the WorkloadIdentity must create the group itself.
Delete the resource group, choose another AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME, or set REQUIRE_OPERATOR_CREATED_WORKLOAD_IDENTITY_RESOURCE_GROUP=false and VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED=false."
    exit 1
  fi
}

assert_key_vault_resource_group_absent() {
  if [[ $ensure_key_vault != "true" ]]; then
    return
  fi

  if az group show -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log ERROR "Resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME already exists.
This e2e test creates and deletes the Key Vault resource group itself.
Delete the resource group or choose another AZURE_KEY_VAULT_RESOURCE_GROUP_NAME."
    exit 1
  fi
}

wait_for_azure_resource_group_deleted() {
  local resource_group=$1
  local timeout=$2
  local success_message=${3:-Azure resource group was deleted}
  local deadline
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while (( SECONDS < deadline )); do
    if ! az group show -n "$resource_group" --query id -o tsv >/dev/null 2>&1; then
      log VERIFY "$success_message: $resource_group"
      return 0
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

install_workload_identity_webhook() {
  local helm_args

  if [[ $install_azure_workload_identity_webhook != "true" ]]; then
    return
  fi

  log INSTALL "Installing Azure Workload Identity mutating webhook"
  helm repo add "$azure_workload_identity_helm_repo_name" "$azure_workload_identity_helm_repo_url" --force-update >/dev/null
  helm repo update >/dev/null

  if ! kubectl get namespace "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
    log CREATE "Creating Azure Workload Identity webhook namespace $azure_workload_identity_webhook_namespace"
    kubectl create namespace "$azure_workload_identity_webhook_namespace" >/dev/null
    created_workload_identity_webhook_namespace=true
  fi

  cleanup_incomplete_webhook_release
  if ! helm status "$azure_workload_identity_webhook_release" -n "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
    created_workload_identity_webhook_release=true
  fi

  helm_args=(
    upgrade --install "$azure_workload_identity_webhook_release" "$azure_workload_identity_helm_chart"
    --namespace "$azure_workload_identity_webhook_namespace"
    --create-namespace
    --set "azureTenantID=$AZURE_TENANT_ID"
    --timeout "$wait_timeout"
  )
  if [[ $azure_workload_identity_openshift_compatibility == "true" ]]; then
    log INSTALL "Using OpenShift-compatible Azure Workload Identity webhook install"
    if ! helm "${helm_args[@]}" --set "replicaCount=$azure_workload_identity_webhook_replica_count"; then
      log ERROR "Failed to install Azure Workload Identity webhook"
      dump_workload_identity_webhook_diagnostics
      exit 1
    fi
    patch_workload_identity_webhook_for_openshift
  else
    helm_args+=(--wait)
    if ! helm "${helm_args[@]}" --set "replicaCount=$azure_workload_identity_webhook_replica_count"; then
      log ERROR "Failed to install Azure Workload Identity webhook"
      dump_workload_identity_webhook_diagnostics
      exit 1
    fi
  fi

  wait_for_workload_identity_webhook
}

patch_workload_identity_webhook_for_openshift() {
  # The upstream chart pins runAsUser/runAsGroup to 65532. OpenShift assigns
  # namespace-scoped UID ranges via SCCs, so remove only those fixed IDs and
  # keep the rest of the container securityContext intact.
  log PATCH "Patching Azure Workload Identity webhook Deployment for OpenShift-assigned UIDs"
  if ! kubectl patch deployment azure-wi-webhook-controller-manager \
    -n "$azure_workload_identity_webhook_namespace" \
    --type=strategic \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"manager","securityContext":{"runAsUser":null,"runAsGroup":null}}]}}}}' >/dev/null; then
    log ERROR "Failed to patch Azure Workload Identity webhook Deployment"
    dump_workload_identity_webhook_diagnostics
    exit 1
  fi
}

wait_for_workload_identity_webhook() {
  log WATCH "Waiting for Azure Workload Identity webhook rollout"
  if ! kubectl rollout status deployment/azure-wi-webhook-controller-manager \
    -n "$azure_workload_identity_webhook_namespace" \
    --timeout="$wait_timeout"; then
    dump_workload_identity_webhook_diagnostics
    exit 1
  fi

  log WATCH "Waiting for Azure Workload Identity webhook pods to become Ready"
  if ! kubectl wait pod -n "$azure_workload_identity_webhook_namespace" \
    -l "release=$azure_workload_identity_webhook_release" \
    --for=condition=Ready \
    --timeout="$wait_timeout"; then
    dump_workload_identity_webhook_diagnostics
    exit 1
  fi
}

dump_workload_identity_webhook_diagnostics() {
  kubectl get all -n "$azure_workload_identity_webhook_namespace" >&2 || true
  kubectl describe pods -n "$azure_workload_identity_webhook_namespace" >&2 || true
  kubectl logs -n "$azure_workload_identity_webhook_namespace" deployment/azure-wi-webhook-controller-manager --tail=200 >&2 || true
  kubectl get events -n "$azure_workload_identity_webhook_namespace" --sort-by=.lastTimestamp >&2 || true
}

cleanup_incomplete_webhook_release() {
  local release_status

  if [[ $cleanup_incomplete_webhook_helm_release != "true" ]]; then
    return
  fi

  release_status=$(helm status "$azure_workload_identity_webhook_release" \
    -n "$azure_workload_identity_webhook_namespace" \
    -o json 2>/dev/null | sed -n 's/.*"status":"\([^"]*\)".*/\1/p' || true)

  case "$release_status" in
    failed|pending-install|pending-upgrade|pending-rollback)
      log DELETE "Removing incomplete Azure Workload Identity webhook Helm release with status $release_status"
      helm uninstall "$azure_workload_identity_webhook_release" \
        -n "$azure_workload_identity_webhook_namespace" \
        --wait \
        --timeout "$wait_timeout" >/dev/null || true
      ;;
  esac
}

install_operator_custom_resource_definitions() {
  if [[ $install_operator_crds != "true" ]]; then
    return
  fi

  log INSTALL "Installing az-workload-identity-operator CRDs"
  make --no-print-directory -C "$repo_root" install
  kubectl wait --for=condition=Established crd/oidcissuers.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
  kubectl wait --for=condition=Established crd/workloadidentities.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
}

wait_for_local_operator() {
  local deadline
  local ready_host
  local ready_url
  deadline=$((SECONDS + $(duration_seconds "$operator_ready_timeout")))
  ready_host=${operator_health_probe_bind_address#http://}
  [[ $ready_host == :* ]] && ready_host="127.0.0.1$ready_host"
  ready_url="http://$ready_host/readyz"

  log WATCH "Waiting for local operator readiness endpoint"
  while ((SECONDS < deadline)); do
    if ! kill -0 "$operator_pid" >/dev/null 2>&1; then
      log ERROR "Local operator exited before it became ready"
      [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
      exit 1
    fi
    if curl -fsS "$ready_url" >/dev/null 2>&1; then
      log READY "Local operator is ready"
      return
    fi
    sleep 2
  done

  log ERROR "Timed out waiting for local operator readiness endpoint"
  [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
  exit 1
}

generate_local_operator_webhook_certificates() {
  local openssl_config
  operator_webhook_cert_dir="$tmpdir/operator-webhook-certs"
  openssl_config="$tmpdir/operator-webhook-openssl.cnf"
  mkdir -p "$operator_webhook_cert_dir"

  cat >"$openssl_config" <<'EOF'
[req]
distinguished_name = req_distinguished_name
x509_extensions = v3_req
prompt = no

[req_distinguished_name]
CN = localhost

[v3_req]
subjectAltName = @alt_names

[alt_names]
DNS.1 = localhost
IP.1 = 127.0.0.1
EOF

  log CREATE "Generating local webhook serving certificate"
  openssl req -x509 -nodes -newkey rsa:2048 \
    -keyout "$operator_webhook_cert_dir/tls.key" \
    -out "$operator_webhook_cert_dir/tls.crt" \
    -days 1 \
    -config "$openssl_config" \
    -extensions v3_req >/dev/null 2>&1
}

start_local_operator() {
  if [[ $run_operator_locally != "true" ]]; then
    return
  fi

  generate_local_operator_webhook_certificates
  operator_log_file=${operator_log_file:-$tmpdir/operator.log}
  log RUN "Starting az-workload-identity-operator locally on health probe $operator_health_probe_bind_address; logs: $operator_log_file"
  (
    cd "$repo_root"
    go run ./cmd/main.go \
      --health-probe-bind-address="$operator_health_probe_bind_address" \
      --webhook-cert-path="$operator_webhook_cert_dir"
  ) >"$operator_log_file" 2>&1 &
  operator_pid=$!
  wait_for_local_operator
}

ensure_key_vault_exists() {
  if [[ $ensure_key_vault != "true" ]]; then
    return
  fi

  log VERIFY "Ensuring Key Vault $KEY_VAULT_NAME exists in resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
  purge_deleted_key_vault || die "Failed to purge soft-deleted Key Vault $KEY_VAULT_NAME"

  if ! az group show -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log CREATE "Creating Key Vault resource group $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
    az group create -n "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" -l "$AZURE_LOCATION" -o none
    created_key_vault_resource_group=true
  fi

  vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv 2>/dev/null || true)
  if [[ -z $vault_id ]]; then
    log CREATE "Creating Key Vault $KEY_VAULT_NAME"
    if ! az keyvault create \
      -n "$KEY_VAULT_NAME" \
      -g "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" \
      -l "$AZURE_LOCATION" \
      --retention-days 7 \
      --enable-rbac-authorization true \
      -o none; then
      die "Failed to create Key Vault $KEY_VAULT_NAME. If purge timed out or the name is globally unavailable, set KEY_VAULT_NAME to another value."
    fi
    vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv)
  else
    vault_resource_group=$(az keyvault show -n "$KEY_VAULT_NAME" --query resourceGroup -o tsv)
    normalized_vault_resource_group=$(printf '%s' "$vault_resource_group" | tr '[:upper:]' '[:lower:]')
    normalized_key_vault_resource_group=$(printf '%s' "$AZURE_KEY_VAULT_RESOURCE_GROUP_NAME" | tr '[:upper:]' '[:lower:]')
    if [[ $normalized_vault_resource_group != "$normalized_key_vault_resource_group" ]]; then
      log ERROR "Key Vault $KEY_VAULT_NAME already exists in resource group $vault_resource_group, but this test expects it in $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
      die "Use another KEY_VAULT_NAME or delete/recreate the vault in $AZURE_KEY_VAULT_RESOURCE_GROUP_NAME"
    fi
  fi

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
    if ! kubectl get serviceaccount default -n "$NAMESPACE" >/dev/null 2>&1; then
      sleep 10
      continue
    fi

    token=$(kubectl create token default -n "$NAMESPACE" --duration=10m 2>/dev/null || true)
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
  oc wait clusteroperator/kube-apiserver --for=condition=Available=True --timeout="$timeout" || return 1
  oc wait clusteroperator/kube-apiserver --for=condition=Progressing=False --timeout="$timeout" || return 1
  oc wait clusteroperator/kube-apiserver --for=condition=Degraded=False --timeout="$timeout" || return 1
}

wait_for_openshift_auth_operators() {
  local timeout=$1
  local operator

  for operator in authentication openshift-apiserver; do
    log WATCH "Waiting for OpenShift $operator operator health"
    oc wait "clusteroperator/$operator" --for=condition=Available=True --timeout="$timeout" || return 1
    oc wait "clusteroperator/$operator" --for=condition=Progressing=False --timeout="$timeout" || return 1
    oc wait "clusteroperator/$operator" --for=condition=Degraded=False --timeout="$timeout" || return 1
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

capture_original_openshift_service_account_issuer() {
  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" ]]; then
    return
  fi

  original_openshift_service_account_issuer=$(openshift_service_account_issuer)
  captured_original_openshift_service_account_issuer=true
  log CAPTURE "Captured original OpenShift serviceAccountIssuer: ${original_openshift_service_account_issuer:-<empty>}"
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

  previous_present=$(kubectl get oidcissuer default -o go-template='{{range $k, $v := .status}}{{if eq $k "previousServiceAccountIssuer"}}true{{end}}{{end}}')
  if [[ $previous_present != "true" ]]; then
    die "OIDCIssuer/default did not record status.previousServiceAccountIssuer"
  fi

  previous_issuer=$(kubectl get oidcissuer default -o go-template='{{index .status "previousServiceAccountIssuer"}}')
  if [[ $previous_issuer != "$original_openshift_service_account_issuer" ]]; then
    die "OIDCIssuer/default status.previousServiceAccountIssuer is '$previous_issuer', want '${original_openshift_service_account_issuer:-<empty>}'"
  fi
}

create_retiring_signing_key_secret() {
  local private_key_file
  local public_key_file

  if [[ $verify_signing_key_rotation != "true" ]]; then
    return
  fi

  private_key_file="$tmpdir/retiring-signing-key.pem"
  public_key_file="$tmpdir/retiring-signing-key.pub"

  log CREATE "Creating retiring signing key Secret $RETIRING_SIGNING_KEY_SECRET_NAMESPACE/$RETIRING_SIGNING_KEY_SECRET_NAME"
  openssl genrsa -out "$private_key_file" 2048 >/dev/null 2>&1
  openssl rsa -in "$private_key_file" -pubout -out "$public_key_file" >/dev/null 2>&1
  kubectl create secret generic "$RETIRING_SIGNING_KEY_SECRET_NAME" \
    -n "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" \
    --from-file="$RETIRING_SIGNING_KEY_SECRET_KEY=$public_key_file" \
    --dry-run=client \
    -o yaml | kubectl apply -f - >/dev/null
  created_retiring_signing_key_secret=true
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
    signing_keys=$(kubectl get oidcissuer default -o jsonpath='{range .status.signingKeys[*]}{.state}{"\n"}{end}' 2>/dev/null || true)
    active_count=$(printf '%s\n' "$signing_keys" | grep -c '^Active$' || true)
    retiring_count=$(printf '%s\n' "$signing_keys" | grep -c '^Retiring$' || true)
    if [[ $active_count == "$expected_active_count" && $retiring_count == "$expected_retiring_count" ]]; then
      log VERIFY "OIDCIssuer/default status.signingKeys contains expected Active/Retiring states"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for OIDCIssuer/default status.signingKeys to contain expected Active/Retiring states"
  kubectl get oidcissuer default -o yaml >&2 || true
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

verify_signing_key_rotation_publish() {
  local issuer_url=$1

  if [[ $verify_signing_key_rotation != "true" ]]; then
    return
  fi

  create_retiring_signing_key_secret
  log PATCH "Adding retiring signing key reference to OIDCIssuer/default"
  kubectl patch oidcissuer default --type=merge -p "{\"spec\":{\"signingKey\":{\"retiringSecretRef\":{\"namespace\":\"$RETIRING_SIGNING_KEY_SECRET_NAMESPACE\",\"name\":\"$RETIRING_SIGNING_KEY_SECRET_NAME\",\"key\":\"$RETIRING_SIGNING_KEY_SECRET_KEY\"}}}}" >/dev/null
  wait_for_oidcissuer_observed_generation "$wait_timeout" || return 1
  kubectl wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout" >/dev/null || return 1
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

  generation=$(kubectl get oidcissuer default -o jsonpath='{.metadata.generation}') || return 1
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  log WATCH "Waiting for OIDCIssuer/default observedGeneration to reach $generation"
  while ((SECONDS < deadline)); do
    observed_generation=$(kubectl get oidcissuer default -o jsonpath='{.status.observedGeneration}' 2>/dev/null || true)
    if [[ $observed_generation =~ ^[0-9]+$ ]] && ((observed_generation >= generation)); then
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for OIDCIssuer/default observedGeneration to reach $generation"
  kubectl get oidcissuer default -o yaml >&2 || true
  return 1
}

verify_openshift_service_account_issuer_handed_off() {
  local current_issuer
  local issuer_url

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi

  issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
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

  issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
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
  kubectl patch oidcissuer default --type=merge -p '{"spec":{"openShift":{"updateServiceAccountIssuer":false}}}' >/dev/null || return 1
  wait_for_oidcissuer_observed_generation "$timeout" || return 1

  log UPDATE "Restoring OpenShift Authentication serviceAccountIssuer to ${original_openshift_service_account_issuer:-<empty>} before deleting OIDCIssuer"
  patch_openshift_service_account_issuer "$original_openshift_service_account_issuer" || return 1

  log WATCH "Waiting for OpenShift API server rollout after manual serviceAccountIssuer handoff"
  wait_for_openshift_api_server_rollout "$original_openshift_service_account_issuer" "$timeout" || return 1
  verify_openshift_service_account_issuer_handed_off || return 1
}

assert_oidcissuer_delete_rejected_by_workload_identity() {
  local delete_output
  local deletion_timestamp

  if [[ $applied_oidc_issuer != "true" || $applied_workload_identity != "true" ]]; then
    return
  fi

  if [[ $run_operator_locally == "true" ]]; then
    log SKIP "Skipping Kubernetes admission integration check because the operator is running locally"
    assert_local_oidcissuer_delete_webhook_handler_rejects_workload_identity
    return
  fi

  log VERIFY "Verifying OIDCIssuer deletion is rejected while WorkloadIdentity exists"
  if delete_output=$(kubectl delete oidcissuer default --wait=false 2>&1); then
    log ERROR "OIDCIssuer/default deletion was accepted while WorkloadIdentity/$WORKLOAD_IDENTITY_NAME still exists"
    kubectl get oidcissuer default -o yaml >&2 || true
    kubectl get workloadidentities -A >&2 || true
    return 1
  fi
  if [[ $delete_output != *"OIDCIssuer deletion is blocked"* ]]; then
    log ERROR "OIDCIssuer/default deletion failed for an unexpected reason: $delete_output"
    return 1
  fi

  deletion_timestamp=$(kubectl get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
  if [[ -n $deletion_timestamp ]]; then
    log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though deletion was rejected"
    kubectl get oidcissuer default -o yaml >&2 || true
    return 1
  fi

  log VERIFY "OIDCIssuer deletion was rejected before the resource entered deletion"
}

oidcissuer_delete_admission_review() {
  local old_object
  old_object=$(kubectl get oidcissuer default -o json)

  cat <<EOF
{
  "apiVersion": "admission.k8s.io/v1",
  "kind": "AdmissionReview",
  "request": {
    "uid": "openshift-e2e-oidcissuer-delete",
    "kind": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "kind": "OIDCIssuer"
    },
    "resource": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "resource": "oidcissuers"
    },
    "name": "default",
    "operation": "DELETE",
    "userInfo": {
      "username": "openshift-e2e"
    },
    "oldObject": $old_object
  }
}
EOF
}

assert_local_oidcissuer_delete_webhook_handler_rejects_workload_identity() {
  local deadline
  local response
  local deletion_timestamp
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log VERIFY "Verifying local OIDCIssuer webhook handler rejects deletion while WorkloadIdentity exists"
  while ((SECONDS < deadline)); do
    response=$(oidcissuer_delete_admission_review | curl -sk -H 'Content-Type: application/json' --data-binary @- "$operator_webhook_url" 2>/dev/null || true)
    if [[ $response == *'"allowed":false'* && $response == *"OIDCIssuer deletion is blocked"* ]]; then
      deletion_timestamp=$(kubectl get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
      if [[ -n $deletion_timestamp ]]; then
        log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though the validating webhook rejected deletion"
        kubectl get oidcissuer default -o yaml >&2 || true
        return 1
      fi
      log VERIFY "Local OIDCIssuer webhook handler rejected deletion without changing the resource"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for local OIDCIssuer webhook handler to reject deletion"
  [[ -n ${response:-} ]] && log ERROR "Last webhook response: $response"
  [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
  kubectl get oidcissuer default -o yaml >&2 || true
  kubectl get workloadidentities -A >&2 || true
  return 1
}

assert_oidcissuer_delete_rejected_by_openshift_service_account_issuer() {
  local current_issuer
  local delete_output
  local deletion_timestamp
  local issuer_url

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" || $captured_original_openshift_service_account_issuer != "true" ]]; then
    return
  fi

  issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}' 2>/dev/null || true)
  if [[ -z $issuer_url ]]; then
    log SKIP "Skipping OIDCIssuer OpenShift handoff guard check because status.issuerURL is empty"
    return
  fi

  current_issuer=$(openshift_service_account_issuer 2>/dev/null || true)
  if [[ $current_issuer != "$issuer_url" ]]; then
    log SKIP "Skipping OIDCIssuer OpenShift handoff guard check because Authentication/cluster no longer references $issuer_url"
    return
  fi

  if [[ $run_operator_locally == "true" ]]; then
    assert_local_oidcissuer_delete_webhook_handler_rejects_openshift_service_account_issuer
    return
  fi

  log VERIFY "Verifying OIDCIssuer deletion is rejected while OpenShift serviceAccountIssuer references $issuer_url"
  if delete_output=$(kubectl delete oidcissuer default --wait=false 2>&1); then
    log ERROR "OIDCIssuer/default deletion was accepted while OpenShift serviceAccountIssuer still references $issuer_url"
    kubectl get oidcissuer default -o yaml >&2 || true
    oc get authentication.config.openshift.io cluster -o yaml >&2 || true
    return 1
  fi
  if [[ $delete_output != *"serviceAccountIssuer still references"* ]]; then
    log ERROR "OIDCIssuer/default deletion failed for an unexpected reason: $delete_output"
    return 1
  fi

  deletion_timestamp=$(kubectl get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
  if [[ -n $deletion_timestamp ]]; then
    log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though deletion was rejected"
    kubectl get oidcissuer default -o yaml >&2 || true
    return 1
  fi

  log VERIFY "OIDCIssuer deletion was rejected before OpenShift serviceAccountIssuer handoff"
}

assert_local_oidcissuer_delete_webhook_handler_rejects_openshift_service_account_issuer() {
  local deadline
  local response
  local deletion_timestamp
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log VERIFY "Verifying local OIDCIssuer webhook handler rejects deletion before OpenShift serviceAccountIssuer handoff"
  while ((SECONDS < deadline)); do
    response=$(oidcissuer_delete_admission_review | curl -sk -H 'Content-Type: application/json' --data-binary @- "$operator_webhook_url" 2>/dev/null || true)
    if [[ $response == *'"allowed":false'* && $response == *"serviceAccountIssuer still references"* ]]; then
      deletion_timestamp=$(kubectl get oidcissuer default -o jsonpath='{.metadata.deletionTimestamp}')
      if [[ -n $deletion_timestamp ]]; then
        log ERROR "OIDCIssuer/default entered deletion with timestamp $deletion_timestamp even though the validating webhook rejected deletion"
        kubectl get oidcissuer default -o yaml >&2 || true
        return 1
      fi
      log VERIFY "Local OIDCIssuer webhook handler rejected deletion before OpenShift handoff"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for local OIDCIssuer webhook handler to reject deletion before OpenShift handoff"
  [[ -n ${response:-} ]] && log ERROR "Last webhook response: $response"
  [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
  kubectl get oidcissuer default -o yaml >&2 || true
  oc get authentication.config.openshift.io cluster -o yaml >&2 || true
  return 1
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
    condition=$(kubectl get workloadidentity "$name" -n "$NAMESPACE" -o jsonpath='{range .status.conditions[?(@.type=="Ready")]}{.status}{"|"}{.reason}{"\n"}{end}' 2>/dev/null || true)
    if [[ $condition == *"False|$reason"* ]]; then
      log VERIFY "WorkloadIdentity/$name reported Ready=False reason $reason"
      return
    fi
    sleep 5
  done

  log ERROR "Timed out waiting for WorkloadIdentity/$name Ready=False reason $reason"
  kubectl get workloadidentity "$name" -n "$NAMESPACE" -o yaml >&2 || true
  return 1
}

verify_workload_identity_conflict_reconciliation() {
  local conflict_name=azwi-sa-conflict
  local conflict_service_account=azwi-sa-conflict
  local federated_credential_conflict_name=azwi-fic-conflict
  local federated_credential_conflict_service_account=azwi-fic-conflict

  if [[ $verify_workload_identity_conflicts != "true" ]]; then
    return
  fi

  log VERIFY "Verifying WorkloadIdentity reconciliation reports ServiceAccount ownership conflicts"
  kubectl create serviceaccount "$conflict_service_account" -n "$NAMESPACE" >/dev/null
  created_conflict_service_account=true
  kubectl label serviceaccount "$conflict_service_account" -n "$NAMESPACE" \
    azure.workload.identity/use=true \
    workloadidentity.azure.micosolutions.se/managed-by=az-workload-identity-operator \
    workloadidentity.azure.micosolutions.se/workload-identity-uid=foreign-workload-identity-uid \
    workloadidentity.azure.micosolutions.se/created-by-operator=false >/dev/null

  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: $conflict_name
  namespace: $NAMESPACE
spec:
  azure:
    subscriptionID: $AZURE_SUBSCRIPTION_ID
    location: $AZURE_LOCATION
    resourceGroupName: $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME
    userAssignedIdentityName: $AZURE_USER_ASSIGNED_IDENTITY_NAME
    federatedIdentityCredentialName: fidc-conflict-safe-reconcile
  serviceAccount:
    name: $conflict_service_account
  deletionPolicy: Retain
EOF
  applied_conflict_workload_identity=true
  wait_for_workloadidentity_ready_false_reason "$conflict_name" ServiceAccountConflict "$wait_timeout" || return 1

  if [[ $run_operator_locally != "true" ]]; then
    log SKIP "Skipping Azure federated credential conflict check because Kubernetes admission may reject the duplicate tuple before reconciliation"
    return
  fi

  log VERIFY "Verifying WorkloadIdentity reconciliation reports Azure federated credential conflicts"
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: workloadidentity.azure.micosolutions.se/v1alpha1
kind: WorkloadIdentity
metadata:
  name: $federated_credential_conflict_name
  namespace: $NAMESPACE
spec:
  azure:
    subscriptionID: $AZURE_SUBSCRIPTION_ID
    location: $AZURE_LOCATION
    resourceGroupName: $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME
    userAssignedIdentityName: $AZURE_USER_ASSIGNED_IDENTITY_NAME
    federatedIdentityCredentialName: $AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME
  serviceAccount:
    name: $federated_credential_conflict_service_account
  deletionPolicy: Retain
EOF
  applied_federated_credential_conflict_workload_identity=true
  wait_for_workloadidentity_ready_false_reason "$federated_credential_conflict_name" FederatedIdentityCredentialConflict "$wait_timeout" || return 1
}

render() {
  local file=$1
  local content
  content=$(<"$file")
  local vars=(
    AZURE_SUBSCRIPTION_ID AZURE_LOCATION AZURE_RESOURCE_GROUP_NAME AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME AZURE_STORAGE_ACCOUNT_NAME AZURE_BLOB_CONTAINER_NAME
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

assert_oidc_resource_group_absent
assert_workload_identity_resource_group_absent
assert_key_vault_resource_group_absent
install_workload_identity_webhook
install_operator_custom_resource_definitions
start_local_operator

if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log CREATE "Creating test namespace $NAMESPACE"
  kubectl create namespace "$NAMESPACE"
  created_test_namespace=true
fi
capture_original_openshift_service_account_issuer
log APPLY "Applying OIDCIssuer/default"
render "$script_dir/oidc-issuer.yaml" | kubectl apply -f -
applied_oidc_issuer=true

if [[ $assign_oidc_storage_blob_role == "true" ]]; then
  operator_principal_id=${OPERATOR_AZURE_PRINCIPAL_ID:-}
  operator_principal_type=${OPERATOR_AZURE_PRINCIPAL_TYPE:-}
  if [[ -z $operator_principal_id ]]; then
    resolve_active_azure_principal
    operator_principal_id=$active_azure_principal_id
    operator_principal_type=$active_azure_principal_type
    log CONFIG "Using active Azure CLI principal for OIDC blob upload role assignment"
  fi
  operator_principal_type=${operator_principal_type:-ServicePrincipal}
  storage_account_id=$(wait_for_storage_account_id)
  ensure_role_assignment "$operator_principal_id" "$operator_principal_type" "$oidc_storage_blob_role" "$storage_account_id"
fi

log WATCH "Waiting for OIDCIssuer/default to become Ready"
kubectl wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout"
issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}')
if [[ -z $issuer_url ]]; then
  die "OIDCIssuer default is missing status.issuerURL"
fi
verify_oidcissuer_captured_previous_service_account_issuer "$issuer_url"
verify_signing_key_rotation_publish "$issuer_url"
wait_for_openshift_api_server_rollout "$issuer_url"

ensure_key_vault_exists

log APPLY "Applying WorkloadIdentity/$WORKLOAD_IDENTITY_NAME"
render "$script_dir/workload-identity.yaml" | kubectl apply -f -
applied_workload_identity=true
log WATCH "Waiting for WorkloadIdentity/$WORKLOAD_IDENTITY_NAME to become Ready"
kubectl wait --for=condition=Ready "workloadidentity/$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"
verify_workload_identity_conflict_reconciliation

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

principal_id=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.status.principalID}')
if [[ -z $principal_id ]]; then
  die "WorkloadIdentity $NAMESPACE/$WORKLOAD_IDENTITY_NAME is missing status.principalID"
fi

if [[ $assign_role == "true" ]]; then
  ensure_role_assignment "$principal_id" ServicePrincipal "$key_vault_role" "$vault_id"
fi

reader_dir="$script_dir/keyvault-secret-reader"
if [[ ! -f "$reader_dir/Dockerfile" ]]; then
  die "Missing Key Vault secret reader app at $reader_dir"
fi

if ! oc get buildconfig "$IMAGE_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  log CREATE "Creating OpenShift BuildConfig/$IMAGE_NAME"
  oc new-build --name="$IMAGE_NAME" --binary --strategy=docker -n "$NAMESPACE" >/dev/null
  created_buildconfig=true
fi
log BUILD "Building Key Vault secret reader image $IMAGE_NAME"
oc start-build "$IMAGE_NAME" --from-dir="$reader_dir" --follow -n "$NAMESPACE"

if kubectl get job "$JOB_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  log DELETE "Deleting previous Job/$JOB_NAME"
  kubectl delete job "$JOB_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false
  kubectl wait --for=delete "job/$JOB_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"
fi
log APPLY "Applying Job/$JOB_NAME"
render "$script_dir/job.yaml" | kubectl apply -f -
applied_job=true

log WATCH "Waiting for Job/$JOB_NAME to complete"
if ! kubectl wait --for=condition=complete "job/$JOB_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"; then
  kubectl logs "job/$JOB_NAME" -n "$NAMESPACE" || true
  exit 1
fi
log READ "Printing Job/$JOB_NAME logs"
kubectl logs "job/$JOB_NAME" -n "$NAMESPACE"
assert_oidcissuer_delete_rejected_by_workload_identity
