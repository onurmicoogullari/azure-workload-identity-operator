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
  OPERATOR_OIDC_ISSUER_REFRESH_INTERVAL       default: 1m
  OPERATOR_WORKLOAD_IDENTITY_REFRESH_INTERVAL default: 1m
  OPERATOR_LOG_FILE                           default: temp file
  OPERATOR_WEBHOOK_URL                        default: https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-oidcissuer
                                                local mode uses this to test webhook handler logic, not API server admission integration
  OPERATOR_RECOVERY_WEBHOOK_URL               default: https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-workloadidentityrecovery
  ENSURE_KEY_VAULT                            default: true
  ENABLE_KEY_VAULT_RBAC                       default: true
  AZURE_RESOURCE_GROUP_NAME                   default: rg-azwi-crc-platform-test (shared platform-owned group)
  AZURE_KEY_VAULT_RESOURCE_GROUP_NAME         default: rg-azwi-crc-kv-test
  AZURE_STORAGE_ACCOUNT_NAME                  default: stazwicrctest
  AZURE_BLOB_CONTAINER_NAME                   default: oidc
  ASSIGN_OIDC_STORAGE_BLOB_ROLE               default: true
  OIDC_STORAGE_BLOB_ROLE                      default: Storage Blob Data Contributor
  OPERATOR_AZURE_PRINCIPAL_ID                 default: active Azure CLI principal object ID
  OPERATOR_AZURE_PRINCIPAL_TYPE               default: inferred from active Azure CLI account, or ServicePrincipal when OPERATOR_AZURE_PRINCIPAL_ID is set
  AZURE_USER_ASSIGNED_IDENTITY_NAME           default: id-azwi-crc-test (suffix; Azure name is NAMESPACE-value)
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
  ./e2e/openshift/e2e-test.sh

The operator creates one shared Azure resource group for OIDCIssuer storage and
WorkloadIdentity resources. The script verifies the operator retains it and
deletes it manually during cleanup. Key Vault uses a separate script-created
group.
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
    1) printf 'Verify the shared Azure resource group is absent and install Azure Workload Identity webhook.' ;;
    2) printf 'Patch webhook deployment for OpenShift UID/SCC compatibility.' ;;
    3) printf 'Install operator CRDs.' ;;
    4) printf 'Start the operator locally.' ;;
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
AZURE_RESOURCE_GROUP_NAME=${AZURE_RESOURCE_GROUP_NAME:-rg-azwi-crc-platform-test}
AZURE_KEY_VAULT_RESOURCE_GROUP_NAME=${AZURE_KEY_VAULT_RESOURCE_GROUP_NAME:-rg-azwi-crc-kv-test}
export AZURE_SUBSCRIPTION_ID
export AZURE_TENANT_ID
export AZURE_LOCATION
export AZURE_RESOURCE_GROUP_NAME
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
operator_oidc_issuer_refresh_interval=${OPERATOR_OIDC_ISSUER_REFRESH_INTERVAL:-1m}
if [[ -n ${OPERATOR_HEALTH_PROBE_BIND_ADDRESS:-} ]]; then
  operator_health_probe_bind_address=$OPERATOR_HEALTH_PROBE_BIND_ADDRESS
else
  if ! operator_health_probe_bind_address=$(available_local_bind_address); then
    exit 1
  fi
fi
operator_log_file=${OPERATOR_LOG_FILE:-}
operator_workload_identity_refresh_interval=${OPERATOR_WORKLOAD_IDENTITY_REFRESH_INTERVAL:-1m}
operator_webhook_url=${OPERATOR_WEBHOOK_URL:-https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-oidcissuer}
operator_recovery_webhook_url=${OPERATOR_RECOVERY_WEBHOOK_URL:-https://127.0.0.1:9443/validate-workloadidentity-azure-micosolutions-se-v1alpha1-workloadidentityrecovery}
ensure_key_vault=${ENSURE_KEY_VAULT:-true}
enable_key_vault_rbac=${ENABLE_KEY_VAULT_RBAC:-true}
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
need openssl
if [[ $run_operator_locally == "true" ]]; then
  need curl
fi
if [[ $verify_signing_key_rotation == "true" ]]; then
  need curl
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
tmpdir=""
operator_pid=""
operator_paused=false
operator_webhook_cert_dir=""
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
applied_duplicate_workload_identity_recovery=false
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
created_workload_identity_webhook_namespace=false
created_workload_identity_webhook_release=false
created_test_namespace=false
created_retiring_signing_key_secret=false
active_azure_principal_id=""
active_azure_principal_type=""
primary_failure=""
cleanup_failed=false
created_key_vault_resource_group=false
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
  if [[ $applied_duplicate_workload_identity_recovery == "true" ]]; then
    cleanup_kubernetes_resource workloadidentityrecovery/"$WORKLOAD_IDENTITY_NAME-recovery-duplicate" "" || return $?
    applied_duplicate_workload_identity_recovery=false
  fi
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
  if [[ $workload_identity_deleted == "true" && $oidc_deleted == "true" ]]; then
    if ! az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
      log ERROR "Shared platform resource group $AZURE_RESOURCE_GROUP_NAME was deleted by the operator"
      return 1
    fi
    log VERIFY "Operator retained shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
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

pause_local_operator() {
  if [[ $run_operator_locally != "true" ]]; then
    return
  fi
  if [[ -z $operator_pid ]] || ! kill -0 "$operator_pid" >/dev/null 2>&1; then
    log ERROR "Cannot pause local operator because its process is not running"
    return 1
  fi
  if ! kill -STOP "$operator_pid"; then
    log ERROR "Could not pause local operator process $operator_pid"
    return 1
  fi
  operator_paused=true
}

resume_local_operator() {
  if [[ $operator_paused != "true" ]]; then
    return
  fi
  if ! kill -CONT "$operator_pid"; then
    log ERROR "Could not resume local operator process $operator_pid"
    return 1
  fi
  operator_paused=false
}

stop_local_operator() {
  local deadline
  if [[ -n $operator_pid ]]; then
    resume_local_operator >/dev/null 2>&1 || true
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
    cleanup_helm_release "$azure_workload_identity_webhook_release" "$azure_workload_identity_webhook_namespace" || return $?
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
    if ! kubernetes_cleanup_command "$wait_timeout" kubectl delete secret "$RETIRING_SIGNING_KEY_SECRET_NAME" -n "$RETIRING_SIGNING_KEY_SECRET_NAMESPACE" --ignore-not-found >/dev/null; then
      kubernetes_cleanup_failure "delete retiring signing key Secret"
      return $?
    fi
  fi
}

cleanup_workload_identity_webhook_namespace() {
  if [[ $created_workload_identity_webhook_namespace == "true" ]]; then
    log DELETE "Deleting Azure Workload Identity webhook namespace $azure_workload_identity_webhook_namespace created by e2e test"
    cleanup_namespace "$azure_workload_identity_webhook_namespace" || return $?
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

cleanup_shared_resource_group() {
  if [[ $cleanup_shared_resource_group_enabled != "true" ]]; then
    return
  fi
  if ! az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    return
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
  get_command=(kubectl get "$resource")
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
  delete_command=(kubectl delete "$resource" --ignore-not-found --wait=false)
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

  log DELETE "Uninstalling Azure Workload Identity webhook Helm release $release"
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

record_workload_identity_webhook_release_cleanup_ownership() {
  local deadline
  local exists_status
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  while (( SECONDS < deadline )); do
    if helm_release_exists_for_cleanup "$azure_workload_identity_webhook_release" "$azure_workload_identity_webhook_namespace"; then
      exists_status=0
    else
      exists_status=$?
    fi
    case "$exists_status" in
      0) return 0 ;;
      1)
        created_workload_identity_webhook_release=true
        return 0
        ;;
      2)
        kubernetes_cleanup_failure "check Helm release $azure_workload_identity_webhook_namespace/$azure_workload_identity_webhook_release before install"
        return $?
        ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying Helm release ownership check"
        sleep_until_deadline "$deadline" 10 || true
        ;;
    esac
  done

  kubernetes_cleanup_failure "check Helm release $azure_workload_identity_webhook_namespace/$azure_workload_identity_webhook_release before install"
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
    release_status=$(printf '%s\n' "$output" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
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

  if ! kubernetes_cleanup_command "$wait_timeout" kubectl delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; then
    kubernetes_cleanup_failure "delete namespace $namespace"
    return $?
  fi

  while (( SECONDS < deadline )); do
    if kubernetes_resource_exists_for_cleanup kubectl get namespace "$namespace" >/dev/null; then
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
    phase=$(kubectl get namespace "$namespace" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    if [[ $phase == "Terminating" ]]; then
      conditions=$(kubectl get namespace "$namespace" -o jsonpath='{range .status.conditions[*]}{.type}={.status}:{.reason}{";"}{end}' 2>/dev/null || true)
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
    sleep_until_deadline "$deadline" 10 || true
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
  cleanup_step "stop local operator" stop_local_operator
  cleanup_step "delete operator-created shared platform resource group" cleanup_shared_resource_group
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

wait_for_shared_resource_group_created() {
  local deadline
  local resource_group_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log WATCH "Waiting for the operator to create shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
  while (( SECONDS < deadline )); do
    resource_group_id=$(az group show \
      --name "$AZURE_RESOURCE_GROUP_NAME" \
      --query id \
      -o tsv 2>/dev/null || true)
    if [[ -n $resource_group_id ]]; then
      log VERIFY "Operator created shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
      return
    fi
    sleep 10
  done

  log ERROR "Timed out waiting for the operator to create shared platform resource group $AZURE_RESOURCE_GROUP_NAME"
  return 1
}

assert_shared_resource_group_absent() {
  if az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    log ERROR "Resource group $AZURE_RESOURCE_GROUP_NAME already exists.
This e2e test verifies that the operator creates the configured shared platform group when it is absent.
Delete the resource group or choose another AZURE_RESOURCE_GROUP_NAME."
    exit 1
  fi
  cleanup_shared_resource_group_enabled=true
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
  record_workload_identity_webhook_release_cleanup_ownership || exit 1

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
    begin_step 2
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
  local deadline
  local status_result
  local release_status

  if [[ $cleanup_incomplete_webhook_helm_release != "true" ]]; then
    return
  fi

  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  while (( SECONDS < deadline )); do
    if release_status=$(helm_release_status_for_cleanup "$azure_workload_identity_webhook_release" "$azure_workload_identity_webhook_namespace"); then
      status_result=0
    else
      status_result=$?
    fi
    case "$status_result" in
      0) break ;;
      1) return ;;
      2) return 1 ;;
      3)
        log RETRY "Kubernetes API/authentication is temporarily unavailable during OpenShift rollout; retrying Helm release status check"
        sleep_until_deadline "$deadline" 10 || true
        ;;
    esac
  done

  if (( SECONDS >= deadline )); then
    kubernetes_cleanup_failure "check Helm release $azure_workload_identity_webhook_namespace/$azure_workload_identity_webhook_release before install"
    return $?
  fi

  case "$release_status" in
    failed|pending-install|pending-upgrade|pending-rollback)
      log DELETE "Removing incomplete Azure Workload Identity webhook Helm release with status $release_status"
      cleanup_helm_release "$azure_workload_identity_webhook_release" "$azure_workload_identity_webhook_namespace" || return $?
      ;;
  esac
}

install_operator_custom_resource_definitions() {
  if [[ $install_operator_crds != "true" ]]; then
    return
  fi

  log INSTALL "Installing azure-workload-identity-operator CRDs"
  make --no-print-directory -C "$repo_root" install
  kubectl wait --for=condition=Established crd/oidcissuers.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
  kubectl wait --for=condition=Established crd/workloadidentities.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
  kubectl wait --for=condition=Established crd/workloadidentityrecoveries.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
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
  local operator_binary

  if [[ $run_operator_locally != "true" ]]; then
    return
  fi

  generate_local_operator_webhook_certificates
  operator_binary="$tmpdir/azure-workload-identity-operator"
  operator_log_file=${operator_log_file:-$tmpdir/operator.log}
  log BUILD "Building local azure-workload-identity-operator binary"
  (
    cd "$repo_root"
    go build -o "$operator_binary" ./cmd/main.go
  )
  log RUN "Starting azure-workload-identity-operator locally on health probe $operator_health_probe_bind_address; logs: $operator_log_file"
  (
    cd "$repo_root"
    exec "$operator_binary" \
      --azure-subscription-id="$AZURE_SUBSCRIPTION_ID" \
      --azure-resource-group-name="$AZURE_RESOURCE_GROUP_NAME" \
      --azure-location="$AZURE_LOCATION" \
      --health-probe-bind-address="$operator_health_probe_bind_address" \
      --oidc-issuer-refresh-interval="$operator_oidc_issuer_refresh_interval" \
      --workload-identity-refresh-interval="$operator_workload_identity_refresh_interval" \
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
      sleep_until_deadline "$deadline" 10 || true
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

  cat >"$build_context/Dockerfile" <<'EOF'
FROM gcr.io/distroless/static:nonroot
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

capture_original_openshift_service_account_issuer() {
  local token

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" ]]; then
    return
  fi

  original_openshift_service_account_issuer=$(openshift_service_account_issuer)
  token=$(kubectl create token default -n default --duration=10m) || {
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
  kubectl create secret generic "$name" \
    -n "$namespace" \
    --from-file="$key=$public_key_file" \
    --dry-run=client \
    -o yaml | kubectl apply -f - >/dev/null
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

oidcissuer_retiring_key_id() {
  kubectl get oidcissuer default -o jsonpath='{range .status.signingKeys[?(@.state=="Retiring")]}{.kid}{"\n"}{end}' 2>/dev/null | sed -n '1p'
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
    generation=$(kubectl get oidcissuer default -o jsonpath='{.metadata.generation}' 2>/dev/null || true)
    if [[ -n $generation && $generation != "$expected_generation" ]]; then
      log ERROR "OIDCIssuer/default generation changed from $expected_generation to $generation while verifying periodic refresh"
      kubectl get oidcissuer default -o yaml >&2 || true
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
  kubectl get oidcissuer default -o yaml >&2 || true
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
    kubectl get oidcissuer default -o yaml >&2 || true
    return 1
  fi
  generation=$(kubectl get oidcissuer default -o jsonpath='{.metadata.generation}')

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
  refresh_openshift_oauth_apiserver_after_issuer_handoff "$timeout" || return 1
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
    workloadidentity.azure.micosolutions.se/managed-by=azure-workload-identity-operator \
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

  log VERIFY "Verifying WorkloadIdentity reconciliation rejects a second owner for an existing Azure identity"
  cat <<EOF | kubectl apply -f - >/dev/null
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
  applied_federated_credential_conflict_workload_identity=true
  wait_for_workloadidentity_ready_false_reason "$federated_credential_conflict_name" AzureResourceOwnershipConflict "$wait_timeout" || return 1
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
  if patch_output=$(kubectl patch workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --type=merge \
    -p "{\"spec\":{\"serviceAccount\":{\"name\":\"${SERVICE_ACCOUNT_NAME}-renamed\"}}}" 2>&1); then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME accepted a ServiceAccount name change"
    return 1
  fi
  if [[ $patch_output != *"field is immutable"* ]]; then
    log ERROR "ServiceAccount name update failed for an unexpected reason: $patch_output"
    return 1
  fi
  configured_name=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.serviceAccount.name}')
  if [[ $configured_name != "$SERVICE_ACCOUNT_NAME" ]]; then
    log ERROR "WorkloadIdentity/$WORKLOAD_IDENTITY_NAME ServiceAccount name changed to $configured_name"
    return 1
  fi

  workload_identity_uid=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
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

  initial_service_account_uid=$(kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  workload_identity_state=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o go-template='{{.metadata.uid}}{{"\t"}}{{.status.serviceAccountUID}}{{"\t"}}{{.status.serviceAccountProvenance}}{{"\t"}}{{.status.clientID}}{{"\t"}}{{.status.tenantID}}')
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

  log PAUSE "Pausing the local operator so the script deterministically creates the replacement ServiceAccount"
  if ! pause_local_operator; then
    return 1
  fi

  log DELETE "Deleting ServiceAccount/$SERVICE_ACCOUNT_NAME to verify same-name recreation"
  if ! kubectl delete serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --wait=true --timeout="$wait_timeout" >/dev/null; then
    resume_local_operator || true
    return 1
  fi

  log CREATE "Recreating ServiceAccount/$SERVICE_ACCOUNT_NAME without managed metadata"
  for create_attempt in {1..10}; do
    if create_output=$(kubectl create serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" 2>&1); then
      replacement_created=true
      break
    fi
    if kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
      log WATCH "Operator recreated ServiceAccount/$SERVICE_ACCOUNT_NAME first; retrying manual replacement"
      if ! kubectl delete serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --wait=true --timeout="$wait_timeout" >/dev/null; then
        resume_local_operator || true
        return 1
      fi
      continue
    fi
    log ERROR "Could not recreate ServiceAccount/$SERVICE_ACCOUNT_NAME: $create_output"
    resume_local_operator || true
    return 1
  done
  if [[ $replacement_created != "true" ]]; then
    log ERROR "Could not win ServiceAccount/$SERVICE_ACCOUNT_NAME recreation race after $create_attempt attempts"
    resume_local_operator || true
    return 1
  fi

  if ! manual_replacement_uid=$(kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}'); then
    resume_local_operator || true
    return 1
  fi
  log UPDATE "Adding benign ServiceAccount metadata drift for reconciliation"
  if ! kubectl patch serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" --type=merge -p '{"metadata":{"labels":{"e2e.azure.micosolutions.se/recreated":"true","workloadidentity.azure.micosolutions.se/created-by-operator":"false"}}}' >/dev/null; then
    resume_local_operator || true
    return 1
  fi
  log RUN "Resuming the local operator to reconcile the replacement ServiceAccount"
  if ! resume_local_operator; then
    return 1
  fi

  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))
  log WATCH "Waiting for WorkloadIdentity reconciliation to preserve provenance and repair ServiceAccount metadata"
  while ((SECONDS < deadline)); do
    workload_identity_state=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o go-template='{{.status.serviceAccountUID}}{{"\t"}}{{.status.serviceAccountProvenance}}' 2>/dev/null || true)
    IFS=$'\t' read -r status_service_account_uid provenance <<<"$workload_identity_state"
    service_account_state=$(kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o go-template='{{.metadata.uid}}{{"\t"}}{{index .metadata.labels "azure.workload.identity/use"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/managed-by"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}{{"\t"}}{{index .metadata.labels "workloadidentity.azure.micosolutions.se/created-by-operator"}}{{"\t"}}{{index .metadata.annotations "azure.workload.identity/client-id"}}{{"\t"}}{{index .metadata.annotations "azure.workload.identity/tenant-id"}}' 2>/dev/null || true)
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
  kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o yaml >&2 || true
  kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" -o yaml >&2 || true
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
    succeeded=$(kubectl get job "$name" -n "$namespace" -o jsonpath='{.status.succeeded}' 2>/dev/null || true)
    failed=$(kubectl get job "$name" -n "$namespace" -o jsonpath='{.status.failed}' 2>/dev/null || true)
    if [[ ${succeeded:-0} =~ ^[0-9]+$ && ${succeeded:-0} -gt 0 ]]; then
      kubectl wait --for=condition=complete "job/$name" -n "$namespace" --timeout=30s >/dev/null || true
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
  kubectl describe "job/$name" -n "$namespace" >&2 || true

  pods=$(kubectl get pods -n "$namespace" -l "job-name=$name" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
  if [[ -z $pods ]]; then
    log ERROR "No Pods found for Job/$name"
    return
  fi

  while IFS= read -r pod; do
    [[ -z $pod ]] && continue
    log READ "Describing Pod/$pod"
    kubectl describe pod "$pod" -n "$namespace" >&2 || true
    log READ "Printing Pod/$pod logs"
    kubectl logs pod/"$pod" -n "$namespace" --all-containers=true >&2 || true
    kubectl logs pod/"$pod" -n "$namespace" --all-containers=true --previous >&2 || true
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
  if [[ $run_operator_locally != "true" ]]; then
    log SKIP "Skipping WorkloadIdentity Azure drift check because the script only controls refresh interval for the local operator"
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

workload_identity_recovery_admission_review() {
  local name=$1
  local current_uid=$2
  local previous_uid=$3
  local object
  object=$(workload_identity_recovery_json "$name" "$current_uid" "$previous_uid")

  cat <<EOF
{
  "apiVersion": "admission.k8s.io/v1",
  "kind": "AdmissionReview",
  "request": {
    "uid": "openshift-e2e-$name",
    "kind": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "kind": "WorkloadIdentityRecovery"
    },
    "resource": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "resource": "workloadidentityrecoveries"
    },
    "name": "$name",
    "operation": "CREATE",
    "userInfo": {
      "username": "openshift-e2e"
    },
    "object": $object
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
    if [[ $run_operator_locally == "true" ]]; then
      output=$(workload_identity_recovery_admission_review "$name" "$current_uid" "$previous_uid" |
        curl -sk -H 'Content-Type: application/json' --data-binary @- "$operator_recovery_webhook_url" 2>/dev/null || true)
      if [[ $output == *'"allowed":false'* && $output == *"$expected"* ]]; then
        return
      fi
      if [[ $retry_until_rejected == "true" &&
        $output == *'"allowed":true'* &&
        $SECONDS -lt $deadline ]]; then
        log RETRY "Recovery admission cache has not observed the existing source UID yet; retrying"
        sleep_until_deadline "$deadline" 2 || true
        continue
      fi
      log ERROR "Recovery creation was not rejected as expected: $output"
      return 1
    fi

    if output=$(workload_identity_recovery_json "$name" "$current_uid" "$previous_uid" |
      kubectl create --dry-run=server -f - 2>&1); then
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
  local output

  if [[ $run_operator_locally != "true" ]]; then
    return
  fi
  output=$(workload_identity_recovery_admission_review "$name" "$current_uid" "$previous_uid" |
    curl -sk -H 'Content-Type: application/json' --data-binary @- "$operator_recovery_webhook_url" 2>/dev/null || true)
  if [[ $output != *'"allowed":true'* ]]; then
    log ERROR "Valid recovery creation was rejected: $output"
    return 1
  fi
}

assert_workload_identity_recovery_delete_allowed() {
  local name=$1
  local old_object
  local output

  if [[ $run_operator_locally != "true" ]]; then
    if ! kubectl delete workloadidentityrecovery "$name" --dry-run=server >/dev/null; then
      log ERROR "WorkloadIdentityRecovery/$name deletion admission was rejected"
      return 1
    fi
    return
  fi

  old_object=$(kubectl get workloadidentityrecovery "$name" -o json)
  output=$(cat <<EOF | curl -sk -H 'Content-Type: application/json' --data-binary @- "$operator_recovery_webhook_url" 2>/dev/null || true
{
  "apiVersion": "admission.k8s.io/v1",
  "kind": "AdmissionReview",
  "request": {
    "uid": "openshift-e2e-delete-$name",
    "kind": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "kind": "WorkloadIdentityRecovery"
    },
    "resource": {
      "group": "workloadidentity.azure.micosolutions.se",
      "version": "v1alpha1",
      "resource": "workloadidentityrecoveries"
    },
    "name": "$name",
    "operation": "DELETE",
    "userInfo": {
      "username": "openshift-e2e"
    },
    "oldObject": $old_object
  }
}
EOF
)
  if [[ $output != *'"allowed":true'* ]]; then
    log ERROR "WorkloadIdentityRecovery/$name deletion admission was rejected: $output"
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
  local duplicate_recovery_uid
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

  current_uid=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  log VERIFY "Verifying recovery cannot be created before WorkloadIdentity enters RecoveryRequired"
  assert_workload_identity_recovery_create_rejected \
    "$recovery_name-early" "$current_uid" "$invalid_uid" "RecoveryRequired" || return 1

  previous_uid=$current_uid
  log UPDATE "Changing WorkloadIdentity deletionPolicy to Retain for controlled recovery"
  kubectl patch workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --type=merge -p '{"spec":{"deletionPolicy":"Retain"}}' >/dev/null
  log DELETE "Deleting WorkloadIdentity/$WORKLOAD_IDENTITY_NAME while retaining its UAMI and ServiceAccount"
  kubectl delete workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
    --wait=true --timeout="$wait_timeout" >/dev/null

  tagged_uid=$(az identity show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --query 'tags."workload-identity-uid"' -o tsv)
  service_account_owner=$(kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" \
    -o go-template='{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}')
  if [[ $tagged_uid != "$previous_uid" || $service_account_owner != "$previous_uid" ]]; then
    log ERROR "Retained UAMI or ServiceAccount lost the previous WorkloadIdentity UID"
    return 1
  fi

  log APPLY "Recreating WorkloadIdentity/$WORKLOAD_IDENTITY_NAME with a new UID"
  render "$script_dir/workload-identity.yaml" | kubectl apply -f - >/dev/null
  current_uid=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')
  if [[ -z $current_uid || $current_uid == "$previous_uid" ]]; then
    log ERROR "Recreated WorkloadIdentity did not receive a new UID"
    return 1
  fi
  wait_for_workloadidentity_ready_false_reason "$WORKLOAD_IDENTITY_NAME" RecoveryRequired "$wait_timeout" || return 1
  reported_previous_uid=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" \
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
  workload_identity_recovery_json "$recovery_name" "$current_uid" "$previous_uid" | kubectl create -f - >/dev/null
  applied_workload_identity_recovery=true

  log VERIFY "Verifying a second recovery for the same source UID is rejected"
  assert_workload_identity_recovery_create_rejected \
    "$duplicate_recovery_name" "$current_uid" "$previous_uid" "already exists" true || return 1

  log CREATE "Bypassing admission to verify the controller rejects a duplicate-source recovery"
  workload_identity_recovery_json \
    "$duplicate_recovery_name" "$current_uid" "$previous_uid" | kubectl create -f - >/dev/null
  applied_duplicate_workload_identity_recovery=true

  if patch_output=$(kubectl patch workloadidentityrecovery "$recovery_name" --type=merge \
    -p "{\"spec\":{\"previousWorkloadIdentityUid\":\"$invalid_uid\"}}" 2>&1); then
    log ERROR "WorkloadIdentityRecovery/$recovery_name accepted a spec change"
    return 1
  fi
  if [[ $patch_output != *"spec is immutable"* ]]; then
    log ERROR "Recovery spec update failed for an unexpected reason: $patch_output"
    return 1
  fi

  log WATCH "Waiting for extra FIC to block recovery without mutation"
  kubectl wait --for=condition=Blocked workloadidentityrecovery/"$recovery_name" --timeout="$wait_timeout"
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

  log WATCH "Waiting for the duplicate-source recovery to fail terminally"
  kubectl wait \
    --for=condition=Failed \
    workloadidentityrecovery/"$duplicate_recovery_name" \
    --timeout="$wait_timeout"
  if [[ $(kubectl get workloadidentityrecovery "$duplicate_recovery_name" \
    -o jsonpath='{.status.conditions[?(@.type=="Failed")].reason}') != "DuplicateRecovery" ]]; then
    log ERROR "Duplicate-source recovery did not fail with reason DuplicateRecovery"
    return 1
  fi
  if [[ -n $(kubectl get workloadidentityrecovery "$duplicate_recovery_name" \
    -o jsonpath='{.status.conditions[?(@.type=="Blocked")].status}') ]]; then
    log ERROR "Terminal duplicate-source recovery retained a contradictory Blocked condition"
    return 1
  fi
  if [[ $(kubectl get workloadidentityrecovery "$duplicate_recovery_name" \
    -o jsonpath='{.status.mutationStarted}') == "true" ]]; then
    log ERROR "Duplicate-source recovery started external mutation"
    return 1
  fi

  log WATCH "Waiting for WorkloadIdentityRecovery/$recovery_name to complete"
  kubectl wait --for=condition=Complete workloadidentityrecovery/"$recovery_name" --timeout="$wait_timeout"
  recovery_uid=$(kubectl get workloadidentityrecovery "$recovery_name" -o jsonpath='{.metadata.uid}')
  duplicate_recovery_uid=$(kubectl get workloadidentityrecovery "$duplicate_recovery_name" -o jsonpath='{.metadata.uid}')
  recovery_plan=$(kubectl get workloadidentityrecovery "$recovery_name" \
    -o go-template='{{.status.plan.userAssignedIdentity.id}}{{"\t"}}{{.status.plan.federatedIdentityCredential.issuer}}{{"\t"}}{{.status.plan.federatedIdentityCredential.subject}}{{"\t"}}{{index .status.plan.federatedIdentityCredential.audiences 0}}')
  IFS=$'\t' read -r plan_identity_id plan_issuer plan_subject plan_audience <<<"$recovery_plan"
  if [[ $(kubectl get workloadidentityrecovery "$recovery_name" -o jsonpath='{.status.mutationStarted}') != "true" ||
    $(kubectl get workloadidentityrecovery "$recovery_name" -o jsonpath='{.status.commitVerified}') != "true" ||
    -z $plan_identity_id ||
    $plan_issuer != "$issuer_url" ||
    $plan_subject != "system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME" ||
    $plan_audience != "api://AzureADTokenExchange" ]]; then
    log ERROR "Recovery completed without the expected forward-only plan and commit checkpoints"
    return 1
  fi

  log WATCH "Waiting for normal WorkloadIdentity reconciliation to resume"
  kubectl wait --for=condition=Ready workloadidentity/"$WORKLOAD_IDENTITY_NAME" \
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
    $last_recovery_tag != "$recovery_uid" ||
    $last_recovery_tag == "$duplicate_recovery_uid" ]]; then
    log ERROR "Committed UAMI recovery tags are incorrect"
    return 1
  fi

  expected_tuple=$issuer_url$'\n'"system:serviceaccount:$NAMESPACE:$SERVICE_ACCOUNT_NAME"$'\n'api://AzureADTokenExchange
  credential_tuple=$(az identity federated-credential show \
    --resource-group "$AZURE_RESOURCE_GROUP_NAME" \
    --identity-name "$AZURE_RESOLVED_USER_ASSIGNED_IDENTITY_NAME" \
    --name "$AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME" \
    --query '[issuer, subject, audiences[0]]' -o tsv)
  service_account_owner=$(kubectl get serviceaccount "$SERVICE_ACCOUNT_NAME" -n "$NAMESPACE" \
    -o go-template='{{index .metadata.labels "workloadidentity.azure.micosolutions.se/workload-identity-uid"}}')
  if [[ $credential_tuple != "$expected_tuple" || $service_account_owner != "$current_uid" ]]; then
    log ERROR "Recovered FIC tuple or ServiceAccount ownership is incorrect"
    return 1
  fi

  log RUN "Re-running Job/$JOB_NAME with the recovered workload identity"
  cleanup_kubernetes_resource "job/$JOB_NAME" "$NAMESPACE" || return 1
  render "$script_dir/job.yaml" | kubectl apply -f - >/dev/null
  if ! wait_for_job_completion "$JOB_NAME" "$NAMESPACE" "$wait_timeout"; then
    dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
    return 1
  fi
  if [[ $(kubectl logs "job/$JOB_NAME" -n "$NAMESPACE") != *"Successfully retrieved secret"* ]]; then
    log ERROR "Recovered workload identity Job did not report successful Key Vault access"
    dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
    return 1
  fi

  controlled_recovery_completed=true
  log DELETE "Deleting terminal WorkloadIdentityRecovery records"
  kubectl delete workloadidentityrecovery \
    "$recovery_name" "$duplicate_recovery_name" \
    --wait=true --timeout="$wait_timeout" >/dev/null
  applied_workload_identity_recovery=false
  applied_duplicate_workload_identity_recovery=false
  log VERIFY "Forward-only controlled recovery completed, rejected the duplicate source, resumed from Blocked, and retained no recovery record after manual deletion"
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

begin_step 1
assert_shared_resource_group_absent
assert_key_vault_resource_group_absent
install_workload_identity_webhook

begin_step 3
install_operator_custom_resource_definitions

begin_step 4
start_local_operator

begin_step 5
if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  log CREATE "Creating test namespace $NAMESPACE"
  kubectl create namespace "$NAMESPACE"
  created_test_namespace=true
fi

begin_step 6
capture_original_openshift_service_account_issuer
log APPLY "Applying OIDCIssuer/default"
render "$script_dir/oidc-issuer.yaml" | kubectl apply -f -
applied_oidc_issuer=true
wait_for_shared_resource_group_created

begin_step 7
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

begin_step 8
log WATCH "Waiting for OIDCIssuer/default to become Ready"
kubectl wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout"
issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}')
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
render "$script_dir/workload-identity.yaml" | kubectl apply -f -
applied_workload_identity=true
log WATCH "Waiting for WorkloadIdentity/$WORKLOAD_IDENTITY_NAME to become Ready"
kubectl wait --for=condition=Ready "workloadidentity/$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"
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

principal_id=$(kubectl get workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" -o jsonpath='{.status.principalID}')
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
oc start-build "$IMAGE_NAME" --from-dir="$reader_build_context" --follow -n "$NAMESPACE"

wait_for_schedulable_node "$wait_timeout"

if kubectl get job "$JOB_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
  log DELETE "Deleting previous Job/$JOB_NAME"
  cleanup_kubernetes_resource "job/$JOB_NAME" "$NAMESPACE"
fi
log APPLY "Applying Job/$JOB_NAME"
render "$script_dir/job.yaml" | kubectl apply -f -
applied_job=true

begin_step 18
log WATCH "Waiting for Job/$JOB_NAME to complete"
if ! wait_for_job_completion "$JOB_NAME" "$NAMESPACE" "$wait_timeout"; then
  dump_job_diagnostics "$JOB_NAME" "$NAMESPACE"
  exit 1
fi
log READ "Printing Job/$JOB_NAME logs"
kubectl logs "job/$JOB_NAME" -n "$NAMESPACE"

begin_step 19
verify_workload_identity_azure_drift_recovery

begin_step 20
verify_workload_identity_controlled_recovery

begin_step 21
assert_oidcissuer_delete_rejected_by_workload_identity
