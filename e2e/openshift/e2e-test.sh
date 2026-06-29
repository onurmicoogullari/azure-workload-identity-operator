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
  RUN_OPERATOR_LOCALLY                        default: true
  OPERATOR_READY_TIMEOUT                      default: WAIT_TIMEOUT
  OPERATOR_LOG_FILE                           default: temp file
  ENSURE_KEY_VAULT                            default: true
  ENABLE_KEY_VAULT_RBAC                       default: true
  AZURE_RESOURCE_GROUP_NAME                   default: rg-azwi-crc-test (OIDCIssuer-owned group)
  AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME default: rg-azwi-crc-wi-test
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
  KEY_VAULT_NAME                              default: kv-azwi-crc-test
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
  OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER     default: true
  OIDC_ISSUER_DELETION_POLICY                 default: Delete
  WORKLOAD_IDENTITY_DELETION_POLICY           default: Delete
  IMAGE_NAME                                  default: azwi-crc-test
  JOB_NAME                                    default: azwi-crc-test
  WAIT_TIMEOUT                                default: 10m
  OPENSHIFT_API_SERVER_ROLLOUT_TIMEOUT        default: WAIT_TIMEOUT
  AZURE_RBAC_PROPAGATION_TIMEOUT              default: 5m
  ASSIGN_KEYVAULT_ROLE                        default: true
  KEY_VAULT_ROLE                              default: Key Vault Secrets User
  KEY_VAULT_READ_TIMEOUT_SECONDS              default: 300

Example:
  ./e2e/openshift/e2e-test.sh

The WorkloadIdentity uses a separate Azure resource group by default so the
OIDCIssuer-owned resource group can be deleted as a whole, including the test
Key Vault created inside it.
EOF
}

if [[ ${1:-} == "-h" || ${1:-} == "--help" ]]; then
  usage
  exit 0
fi

log() {
  printf '%s\n' "$*" >&2
}

die() {
  log "$*"
  exit 1
}

need() {
  command -v "$1" >/dev/null || die "missing required command: $1"
}

need kubectl
need oc
need az

export AZURE_SUBSCRIPTION_ID=${AZURE_SUBSCRIPTION_ID:-$(az account show --query id -o tsv)}
export AZURE_TENANT_ID=${AZURE_TENANT_ID:-$(az account show --query tenantId -o tsv)}
export AZURE_LOCATION=${AZURE_LOCATION:-swedencentral}
export AZURE_RESOURCE_GROUP_NAME=${AZURE_RESOURCE_GROUP_NAME:-rg-azwi-crc-test}
export AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME=${AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME:-rg-azwi-crc-wi-test}
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
run_operator_locally=${RUN_OPERATOR_LOCALLY:-true}
operator_ready_timeout=${OPERATOR_READY_TIMEOUT:-${WAIT_TIMEOUT:-10m}}
operator_log_file=${OPERATOR_LOG_FILE:-}
ensure_key_vault=${ENSURE_KEY_VAULT:-true}
enable_key_vault_rbac=${ENABLE_KEY_VAULT_RBAC:-true}
verify_oidc_resource_group_deleted=${VERIFY_OIDC_RESOURCE_GROUP_DELETED:-$ensure_key_vault}
require_operator_created_oidc_resource_group=${REQUIRE_OPERATOR_CREATED_OIDC_RESOURCE_GROUP:-$verify_oidc_resource_group_deleted}
verify_workload_identity_resource_group_deleted=${VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED:-true}
require_operator_created_workload_identity_resource_group=${REQUIRE_OPERATOR_CREATED_WORKLOAD_IDENTITY_RESOURCE_GROUP:-$verify_workload_identity_resource_group_deleted}
export AZURE_STORAGE_ACCOUNT_NAME=${AZURE_STORAGE_ACCOUNT_NAME:-stazwicrctest}
export AZURE_BLOB_CONTAINER_NAME=${AZURE_BLOB_CONTAINER_NAME:-oidc}
assign_oidc_storage_blob_role=${ASSIGN_OIDC_STORAGE_BLOB_ROLE:-true}
oidc_storage_blob_role=${OIDC_STORAGE_BLOB_ROLE:-Storage Blob Data Contributor}
export AZURE_USER_ASSIGNED_IDENTITY_NAME=${AZURE_USER_ASSIGNED_IDENTITY_NAME:-id-azwi-crc-test}
export AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME=${AZURE_FEDERATED_IDENTITY_CREDENTIAL_NAME:-fidc-azwi-crc-test}
export KEY_VAULT_NAME=${KEY_VAULT_NAME:-kv-azwi-crc-test}
export KEY_VAULT_SECRET_NAME=${KEY_VAULT_SECRET_NAME:-test-secret}
key_vault_secret_value=${KEY_VAULT_SECRET_VALUE:-"azwi-crc-test smoke secret $(date -u +%Y-%m-%dT%H:%M:%SZ)"}
upload_key_vault_secret=${UPLOAD_KEYVAULT_SECRET:-true}
assign_key_vault_secret_writer_role=${ASSIGN_KEYVAULT_SECRET_WRITER_ROLE:-true}
key_vault_secret_writer_role=${KEY_VAULT_SECRET_WRITER_ROLE:-Key Vault Secrets Officer}
export NAMESPACE=${NAMESPACE:-azwi-crc-test}
export WORKLOAD_IDENTITY_NAME=${WORKLOAD_IDENTITY_NAME:-azwi-crc-test}
export SERVICE_ACCOUNT_NAME=${SERVICE_ACCOUNT_NAME:-$WORKLOAD_IDENTITY_NAME}
export SIGNING_KEY_SECRET_NAMESPACE=${SIGNING_KEY_SECRET_NAMESPACE:-openshift-kube-apiserver}
export SIGNING_KEY_SECRET_NAME=${SIGNING_KEY_SECRET_NAME:-bound-service-account-signing-key}
export SIGNING_KEY_SECRET_KEY=${SIGNING_KEY_SECRET_KEY:-service-account.pub}
export OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER=${OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER:-true}
export OIDC_ISSUER_DELETION_POLICY=${OIDC_ISSUER_DELETION_POLICY:-Delete}
export WORKLOAD_IDENTITY_DELETION_POLICY=${WORKLOAD_IDENTITY_DELETION_POLICY:-Delete}
export IMAGE_NAME=${IMAGE_NAME:-azwi-crc-test}
export JOB_NAME=${JOB_NAME:-azwi-crc-test}
export KEY_VAULT_READ_TIMEOUT_SECONDS=${KEY_VAULT_READ_TIMEOUT_SECONDS:-300}

az account set --subscription "$AZURE_SUBSCRIPTION_ID"

wait_timeout=${WAIT_TIMEOUT:-10m}
azure_rbac_propagation_timeout=${AZURE_RBAC_PROPAGATION_TIMEOUT:-5m}
assign_role=${ASSIGN_KEYVAULT_ROLE:-true}
key_vault_role=${KEY_VAULT_ROLE:-Key Vault Secrets User}

[[ $install_azure_workload_identity_webhook == "true" ]] && need helm
if [[ $install_operator_crds == "true" || $run_operator_locally == "true" ]]; then
  need make
  need go
fi
[[ $run_operator_locally == "true" ]] && need curl

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
tmpdir=""
operator_pid=""
vault_id=""
applied_oidc_issuer=false
applied_workload_identity=false
created_buildconfig=false
applied_job=false
oidc_deleted=false
workload_identity_deleted=false
created_workload_identity_webhook_namespace=false
created_workload_identity_webhook_release=false
created_test_namespace=false
active_azure_principal_id=""
active_azure_principal_type=""

cleanup_job() {
  [[ $applied_job == "true" ]] && kubectl delete job "$JOB_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
}

cleanup_workload_identity() {
  workload_identity_deleted=false
  if [[ $applied_workload_identity == "true" ]]; then
    if [[ $verify_workload_identity_resource_group_deleted == "true" && $WORKLOAD_IDENTITY_DELETION_POLICY == "Delete" && $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME != "$AZURE_RESOURCE_GROUP_NAME" ]]; then
      log "Waiting for Azure resource group $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME to be deleted by operator cleanup"
    fi
    kubectl delete workloadidentity "$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
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
      log "Skipping separate WorkloadIdentity resource group deletion verification because it matches the OIDCIssuer resource group"
    else
      wait_for_azure_resource_group_deleted "$AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME" "$wait_timeout"
    fi
  fi
}

cleanup_oidc_issuer() {
  oidc_deleted=false
  if [[ $applied_oidc_issuer == "true" ]]; then
    if [[ $verify_oidc_resource_group_deleted == "true" && $OIDC_ISSUER_DELETION_POLICY == "Delete" ]]; then
      log "Waiting for Azure resource group $AZURE_RESOURCE_GROUP_NAME to be deleted by operator cleanup"
    fi
    kubectl delete oidcissuer default --ignore-not-found >/dev/null 2>&1
    if kubectl wait --for=delete oidcissuer/default --timeout="$wait_timeout" >/dev/null 2>&1; then
      oidc_deleted=true
    else
      return 1
    fi
  fi
}

verify_oidc_resource_group_cleanup() {
  if [[ $oidc_deleted == "true" && $verify_oidc_resource_group_deleted == "true" && $OIDC_ISSUER_DELETION_POLICY == "Delete" ]]; then
    wait_for_azure_resource_group_deleted "$AZURE_RESOURCE_GROUP_NAME" "$wait_timeout"
  fi
}

cleanup_build_artifacts() {
  if [[ $created_buildconfig == "true" ]]; then
    oc delete buildconfig "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    oc delete imagestream "$IMAGE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
  fi
}

stop_local_operator() {
  if [[ -n $operator_pid ]]; then
    pkill -TERM -P "$operator_pid" >/dev/null 2>&1
    kill "$operator_pid" >/dev/null 2>&1
    wait "$operator_pid" >/dev/null 2>&1
  fi
}

cleanup_workload_identity_webhook_release() {
  if [[ $created_workload_identity_webhook_release == "true" ]]; then
    if helm status "$azure_workload_identity_webhook_release" -n "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
      log "Uninstalling Azure Workload Identity webhook Helm release $azure_workload_identity_webhook_release"
      helm uninstall "$azure_workload_identity_webhook_release" \
        -n "$azure_workload_identity_webhook_namespace" \
        --wait \
        --timeout "$wait_timeout" >/dev/null 2>&1
    fi
  fi
}

cleanup_test_namespace() {
  if [[ $created_test_namespace == "true" ]]; then
    log "Deleting test namespace $NAMESPACE created by e2e test"
    kubectl delete namespace "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    kubectl wait --for=delete "namespace/$NAMESPACE" --timeout="$wait_timeout" >/dev/null 2>&1
  fi
}

cleanup_workload_identity_webhook_namespace() {
  if [[ $created_workload_identity_webhook_namespace == "true" ]]; then
    log "Deleting Azure Workload Identity webhook namespace $azure_workload_identity_webhook_namespace created by e2e test"
    kubectl delete namespace "$azure_workload_identity_webhook_namespace" --ignore-not-found >/dev/null 2>&1
    kubectl wait --for=delete "namespace/$azure_workload_identity_webhook_namespace" --timeout="$wait_timeout" >/dev/null 2>&1
  fi
}

cleanup_tmpdir() {
  [[ -n $tmpdir ]] && rm -rf "$tmpdir"
}

cleanup() {
  local exit_code=$?
  set +e

  cleanup_job || exit_code=1
  cleanup_workload_identity || exit_code=1
  if [[ $exit_code -eq 0 ]]; then
    verify_workload_identity_resource_group_cleanup || exit_code=1
  fi
  cleanup_oidc_issuer || exit_code=1
  if [[ $exit_code -eq 0 ]]; then
    verify_oidc_resource_group_cleanup || exit_code=1
  fi
  cleanup_build_artifacts || exit_code=1
  stop_local_operator || exit_code=1
  cleanup_workload_identity_webhook_release || exit_code=1
  cleanup_test_namespace || exit_code=1
  cleanup_workload_identity_webhook_namespace || exit_code=1
  cleanup_tmpdir

  exit "$exit_code"
}
trap cleanup EXIT

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
  local existing_id

  existing_id=$(az role assignment list --assignee "$principal_id" --role "$role" --scope "$scope" --query '[0].id' -o tsv)
  if [[ -n $existing_id ]]; then
    log "Role assignment already exists: role '$role' on '$scope'"
    return
  fi

  log "Creating role assignment: role '$role' on '$scope'"
  az role assignment create \
    --assignee-object-id "$principal_id" \
    --assignee-principal-type "$principal_type" \
    --role "$role" \
    --scope "$scope" \
    --query id \
    -o tsv >/dev/null
}

wait_for_storage_account_id() {
  local deadline
  local storage_account_id
  deadline=$((SECONDS + $(duration_seconds "$wait_timeout")))

  log "Waiting for storage account $AZURE_RESOURCE_GROUP_NAME/$AZURE_STORAGE_ACCOUNT_NAME"
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

  log "Timed out waiting for storage account $AZURE_RESOURCE_GROUP_NAME/$AZURE_STORAGE_ACCOUNT_NAME"
  kubectl describe oidcissuer default >&2 || true
  exit 1
}

assert_oidc_resource_group_absent() {
  if [[ $require_operator_created_oidc_resource_group != "true" ]]; then
    return
  fi

  if az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
    cat >&2 <<EOF
Resource group $AZURE_RESOURCE_GROUP_NAME already exists.
This e2e test is configured to verify OIDCIssuer-created resource group deletion, so the OIDCIssuer must create the group itself.
Delete the resource group, choose another AZURE_RESOURCE_GROUP_NAME, or set REQUIRE_OPERATOR_CREATED_OIDC_RESOURCE_GROUP=false and VERIFY_OIDC_RESOURCE_GROUP_DELETED=false.
EOF
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
    cat >&2 <<EOF
Resource group $AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME already exists.
This e2e test is configured to verify WorkloadIdentity-created resource group deletion, so the WorkloadIdentity must create the group itself.
Delete the resource group, choose another AZURE_WORKLOAD_IDENTITY_RESOURCE_GROUP_NAME, or set REQUIRE_OPERATOR_CREATED_WORKLOAD_IDENTITY_RESOURCE_GROUP=false and VERIFY_WORKLOAD_IDENTITY_RESOURCE_GROUP_DELETED=false.
EOF
    exit 1
  fi
}

wait_for_azure_resource_group_deleted() {
  local resource_group=$1
  local timeout=$2
  local deadline
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  while (( SECONDS < deadline )); do
    if ! az group show -n "$resource_group" --query id -o tsv >/dev/null 2>&1; then
      log "Azure resource group $resource_group was deleted"
      return 0
    fi
    sleep 15
  done

  log "Timed out waiting for Azure resource group $resource_group to be deleted"
  az resource list -g "$resource_group" -o table >&2 || true
  return 1
}

upload_key_vault_secret_with_retry() {
  local deadline
  local output
  deadline=$((SECONDS + $(duration_seconds "$azure_rbac_propagation_timeout")))

  log "Uploading Key Vault secret $KEY_VAULT_SECRET_NAME to $KEY_VAULT_NAME"
  while true; do
    if output=$(az keyvault secret set \
      --vault-name "$KEY_VAULT_NAME" \
      --name "$KEY_VAULT_SECRET_NAME" \
      --value "$key_vault_secret_value" \
      --query id \
      -o tsv 2>&1); then
      log "Uploaded Key Vault secret: $output"
      return
    fi

    if (( SECONDS >= deadline )); then
      log "Timed out uploading Key Vault secret after waiting for Azure RBAC propagation"
      log "$output"
      exit 1
    fi

    log "Key Vault secret upload is not authorized yet; retrying"
    sleep 10
  done
}

install_workload_identity_webhook() {
  local helm_args

  if [[ $install_azure_workload_identity_webhook != "true" ]]; then
    return
  fi

  log "Installing Azure Workload Identity mutating webhook"
  helm repo add "$azure_workload_identity_helm_repo_name" "$azure_workload_identity_helm_repo_url" --force-update >/dev/null
  helm repo update >/dev/null

  if ! kubectl get namespace "$azure_workload_identity_webhook_namespace" >/dev/null 2>&1; then
    log "Creating Azure Workload Identity webhook namespace $azure_workload_identity_webhook_namespace"
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
    log "Using OpenShift-compatible Azure Workload Identity webhook install"
    if ! helm "${helm_args[@]}" --set "replicaCount=$azure_workload_identity_webhook_replica_count"; then
      log "Failed to install Azure Workload Identity webhook"
      dump_workload_identity_webhook_diagnostics
      exit 1
    fi
    patch_workload_identity_webhook_for_openshift
  else
    helm_args+=(--wait)
    if ! helm "${helm_args[@]}" --set "replicaCount=$azure_workload_identity_webhook_replica_count"; then
      log "Failed to install Azure Workload Identity webhook"
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
  log "Patching Azure Workload Identity webhook Deployment for OpenShift-assigned UIDs"
  if ! kubectl patch deployment azure-wi-webhook-controller-manager \
    -n "$azure_workload_identity_webhook_namespace" \
    --type=strategic \
    -p '{"spec":{"template":{"spec":{"containers":[{"name":"manager","securityContext":{"runAsUser":null,"runAsGroup":null}}]}}}}' >/dev/null; then
    log "Failed to patch Azure Workload Identity webhook Deployment"
    dump_workload_identity_webhook_diagnostics
    exit 1
  fi
}

wait_for_workload_identity_webhook() {
  log "Waiting for Azure Workload Identity webhook rollout"
  if ! kubectl rollout status deployment/azure-wi-webhook-controller-manager \
    -n "$azure_workload_identity_webhook_namespace" \
    --timeout="$wait_timeout"; then
    dump_workload_identity_webhook_diagnostics
    exit 1
  fi

  log "Waiting for Azure Workload Identity webhook pods to become Ready"
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
      log "Removing incomplete Azure Workload Identity webhook Helm release with status $release_status"
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

  log "Installing az-workload-identity-operator CRDs"
  make --no-print-directory -C "$repo_root" install
  kubectl wait --for=condition=Established crd/oidcissuers.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
  kubectl wait --for=condition=Established crd/workloadidentities.workloadidentity.azure.micosolutions.se --timeout="$wait_timeout"
}

wait_for_local_operator() {
  local deadline
  deadline=$((SECONDS + $(duration_seconds "$operator_ready_timeout")))

  log "Waiting for local operator readiness endpoint"
  while ((SECONDS < deadline)); do
    if ! kill -0 "$operator_pid" >/dev/null 2>&1; then
      log "Local operator exited before it became ready"
      [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
      exit 1
    fi
    if curl -fsS http://127.0.0.1:8081/readyz >/dev/null 2>&1; then
      log "Local operator is ready"
      return
    fi
    sleep 2
  done

  log "Timed out waiting for local operator readiness endpoint"
  [[ -n $operator_log_file ]] && sed -n '1,220p' "$operator_log_file" >&2
  exit 1
}

start_local_operator() {
  if [[ $run_operator_locally != "true" ]]; then
    return
  fi

  operator_log_file=${operator_log_file:-$tmpdir/operator.log}
  log "Starting az-workload-identity-operator locally; logs: $operator_log_file"
  make --no-print-directory -C "$repo_root" run >"$operator_log_file" 2>&1 &
  operator_pid=$!
  wait_for_local_operator
}

ensure_key_vault_exists_in_oidc_resource_group() {
  if [[ $ensure_key_vault != "true" ]]; then
    return
  fi

  log "Ensuring Key Vault $KEY_VAULT_NAME exists in OIDCIssuer resource group $AZURE_RESOURCE_GROUP_NAME"
  vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv 2>/dev/null || true)
  if [[ -z $vault_id ]]; then
    if ! az group show -n "$AZURE_RESOURCE_GROUP_NAME" --query id -o tsv >/dev/null 2>&1; then
      die "OIDCIssuer resource group $AZURE_RESOURCE_GROUP_NAME does not exist; the OIDCIssuer must create it before Key Vault creation"
    fi
    log "Creating Key Vault $KEY_VAULT_NAME"
    if ! az keyvault create \
      -n "$KEY_VAULT_NAME" \
      -g "$AZURE_RESOURCE_GROUP_NAME" \
      -l "$AZURE_LOCATION" \
      --enable-rbac-authorization true \
      -o none; then
      die "Failed to create Key Vault $KEY_VAULT_NAME. If the name is soft-deleted or globally unavailable, set KEY_VAULT_NAME to another value."
    fi
    vault_id=$(az keyvault show -n "$KEY_VAULT_NAME" --query id -o tsv)
  else
    vault_resource_group=$(az keyvault show -n "$KEY_VAULT_NAME" --query resourceGroup -o tsv)
    normalized_vault_resource_group=$(printf '%s' "$vault_resource_group" | tr '[:upper:]' '[:lower:]')
    normalized_oidc_resource_group=$(printf '%s' "$AZURE_RESOURCE_GROUP_NAME" | tr '[:upper:]' '[:lower:]')
    if [[ $normalized_vault_resource_group != "$normalized_oidc_resource_group" ]]; then
      log "Key Vault $KEY_VAULT_NAME already exists in resource group $vault_resource_group, but this test expects it in OIDCIssuer resource group $AZURE_RESOURCE_GROUP_NAME"
      die "Use another KEY_VAULT_NAME or delete/recreate the vault in $AZURE_RESOURCE_GROUP_NAME"
    fi
  fi

  if [[ $enable_key_vault_rbac == "true" ]]; then
    key_vault_rbac_enabled=$(az keyvault show -n "$KEY_VAULT_NAME" --query properties.enableRbacAuthorization -o tsv)
    case "$key_vault_rbac_enabled" in
      true|True|TRUE) ;;
      *)
        log "Enabling Azure RBAC authorization on Key Vault $KEY_VAULT_NAME"
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
  value=${value//_//}

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

  log "Waiting for newly issued service account tokens to use issuer $issuer_url"
  while ((SECONDS < deadline)); do
    if ! kubectl get serviceaccount default -n "$NAMESPACE" >/dev/null 2>&1; then
      sleep 10
      continue
    fi

    token=$(kubectl create token default -n "$NAMESPACE" --duration=10m 2>/dev/null || true)
    if [[ -n $token ]]; then
      token_issuer=$(jwt_issuer "$token" || true)
      if [[ $token_issuer == "$issuer_url" ]]; then
        log "OpenShift API server is issuing service account tokens with issuer $issuer_url"
        return
      fi
      if [[ -n $token_issuer ]]; then
        log "Current service account token issuer is $token_issuer; waiting for $issuer_url"
      fi
    fi
    sleep 10
  done

  log "Timed out waiting for service account tokens to use issuer $issuer_url"
  oc get clusteroperator kube-apiserver >&2 || true
  oc describe clusteroperator kube-apiserver >&2 || true
  oc get authentication.config.openshift.io cluster -o yaml >&2 || true
  exit 1
}

wait_for_openshift_api_server_rollout() {
  local issuer_url=$1
  local timeout=${OPENSHIFT_API_SERVER_ROLLOUT_TIMEOUT:-$wait_timeout}
  local deadline
  local configured_issuer
  deadline=$((SECONDS + $(duration_seconds "$timeout")))

  if [[ $OPENSHIFT_UPDATE_SERVICE_ACCOUNT_ISSUER != "true" ]]; then
    return
  fi

  log "Waiting for OpenShift Authentication serviceAccountIssuer to become $issuer_url"
  while ((SECONDS < deadline)); do
    configured_issuer=$(oc get authentication.config.openshift.io cluster -o jsonpath='{.spec.serviceAccountIssuer}' 2>/dev/null || true)
    if [[ $configured_issuer == "$issuer_url" ]]; then
      break
    fi
    sleep 10
  done
  if [[ $configured_issuer != "$issuer_url" ]]; then
    log "Timed out waiting for Authentication/cluster serviceAccountIssuer to become $issuer_url"
    oc get authentication.config.openshift.io cluster -o yaml >&2 || true
    exit 1
  fi

  log "Waiting for OpenShift kube-apiserver operator rollout"
  oc wait clusteroperator/kube-apiserver --for=condition=Available=True --timeout="$timeout"
  oc wait clusteroperator/kube-apiserver --for=condition=Progressing=False --timeout="$timeout"
  oc wait clusteroperator/kube-apiserver --for=condition=Degraded=False --timeout="$timeout"

  wait_for_service_account_token_issuer "$issuer_url" "$timeout"

  log "Waiting for OpenShift kube-apiserver operator to settle after token issuer update"
  oc wait clusteroperator/kube-apiserver --for=condition=Available=True --timeout="$timeout"
  oc wait clusteroperator/kube-apiserver --for=condition=Progressing=False --timeout="$timeout"
  oc wait clusteroperator/kube-apiserver --for=condition=Degraded=False --timeout="$timeout"
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
install_workload_identity_webhook
install_operator_custom_resource_definitions
start_local_operator

if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
  kubectl create namespace "$NAMESPACE"
  created_test_namespace=true
fi
render "$script_dir/oidc-issuer.yaml" | kubectl apply -f -
applied_oidc_issuer=true

if [[ $assign_oidc_storage_blob_role == "true" ]]; then
  operator_principal_id=${OPERATOR_AZURE_PRINCIPAL_ID:-}
  operator_principal_type=${OPERATOR_AZURE_PRINCIPAL_TYPE:-}
  if [[ -z $operator_principal_id ]]; then
    resolve_active_azure_principal
    operator_principal_id=$active_azure_principal_id
    operator_principal_type=$active_azure_principal_type
    log "Using active Azure CLI principal for OIDC blob upload role assignment"
  fi
  operator_principal_type=${operator_principal_type:-ServicePrincipal}
  storage_account_id=$(wait_for_storage_account_id)
  ensure_role_assignment "$operator_principal_id" "$operator_principal_type" "$oidc_storage_blob_role" "$storage_account_id"
  kubectl annotate oidcissuer default "e2e.workloadidentity.azure.micosolutions.se/reconcile-at=$(date -u +%Y%m%d%H%M%S)" --overwrite >/dev/null
fi

kubectl wait --for=condition=Ready oidcissuer/default --timeout="$wait_timeout"
issuer_url=$(kubectl get oidcissuer default -o jsonpath='{.status.issuerURL}')
if [[ -z $issuer_url ]]; then
  die "OIDCIssuer default is missing status.issuerURL"
fi
wait_for_openshift_api_server_rollout "$issuer_url"

ensure_key_vault_exists_in_oidc_resource_group

render "$script_dir/workload-identity.yaml" | kubectl apply -f -
applied_workload_identity=true
kubectl wait --for=condition=Ready "workloadidentity/$WORKLOAD_IDENTITY_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"

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
  oc new-build --name="$IMAGE_NAME" --binary --strategy=docker -n "$NAMESPACE" >/dev/null
  created_buildconfig=true
fi
oc start-build "$IMAGE_NAME" --from-dir="$reader_dir" --follow -n "$NAMESPACE"

kubectl delete job "$JOB_NAME" -n "$NAMESPACE" --ignore-not-found
render "$script_dir/job.yaml" | kubectl apply -f -
applied_job=true

if ! kubectl wait --for=condition=complete "job/$JOB_NAME" -n "$NAMESPACE" --timeout="$wait_timeout"; then
  kubectl logs "job/$JOB_NAME" -n "$NAMESPACE" || true
  exit 1
fi
kubectl logs "job/$JOB_NAME" -n "$NAMESPACE"
