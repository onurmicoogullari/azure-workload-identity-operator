#!/usr/bin/env bash

# Copyright 2026.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

: "${CONTAINER_TOOL:=docker}"
: "${GIT:=git}"
: "${HELM:=helm}"
: "${KIND:=kind}"
: "${KUBECTL:=kubectl}"
: "${TAR:=tar}"
: "${KIND_CLUSTER:=azure-workload-identity-operator-upgrade-e2e}"
: "${KIND_EXPERIMENTAL_PROVIDER:=docker}"
: "${KIND_UPGRADE_EXPECTED_VERSION:=v0.32.0}"
: "${HELM_UPGRADE_EXPECTED_VERSION:=v4.2.3}"
: "${UPGRADE_E2E_KEEP_CLUSTER:=false}"
export CONTAINER_TOOL GIT HELM KIND KUBECTL TAR KIND_CLUSTER KIND_EXPERIMENTAL_PROVIDER

required_variables=(
  UPGRADE_E2E_BASELINE_REF
  UPGRADE_E2E_CANDIDATE_REF
  KIND_UPGRADE_NODE_IMAGE
)
for variable in "${required_variables[@]}"; do
  if [[ -z "${!variable:-}" ]]; then
    echo "$variable is required." >&2
    exit 1
  fi
done

state_dir="$(mktemp -d "${TMPDIR:-/tmp}/azure-workload-identity-kind-upgrade.XXXXXX")"
chmod 700 "$state_dir"
export KUBECONFIG="$state_dir/kubeconfig"
export HELM_CONFIG_HOME="$state_dir/helm/config"
export HELM_CACHE_HOME="$state_dir/helm/cache"
export HELM_DATA_HOME="$state_dir/helm/data"
export UPGRADE_E2E_RUN_ID="${state_dir##*.}"
mkdir -p "$HELM_CONFIG_HOME" "$HELM_CACHE_HOME" "$HELM_DATA_HOME"

cluster_owned=false
cleanup_status=0
preserve_state=false

cleanup() {
  local status=$?
  trap - EXIT INT TERM

  if [[ "$cluster_owned" == "true" && "$UPGRADE_E2E_KEEP_CLUSTER" != "true" ]]; then
    echo "Deleting owned Kind cluster '$KIND_CLUSTER'..."
    if ! "$KIND" delete cluster --name "$KIND_CLUSTER"; then
      echo "Failed to delete Kind cluster '$KIND_CLUSTER'." >&2
      cleanup_status=1
    fi
  elif [[ "$cluster_owned" == "true" ]]; then
    echo "Keeping Kind cluster '$KIND_CLUSTER' because UPGRADE_E2E_KEEP_CLUSTER=true."
    echo "Inspect it with: export KUBECONFIG=$KUBECONFIG"
    preserve_state=true
  fi

  if [[ "$cleanup_status" -ne 0 ]]; then
    preserve_state=true
  fi
  if [[ "$preserve_state" == "true" ]]; then
    echo "Preserving isolated test state at $state_dir"
  else
    rm -rf -- "$state_dir"
  fi

  if [[ "$status" -eq 0 && "$cleanup_status" -ne 0 ]]; then
    status=$cleanup_status
  fi
  exit "$status"
}

trap cleanup EXIT
trap 'exit 130' INT TERM

if [[ "$UPGRADE_E2E_KEEP_CLUSTER" != "true" && "$UPGRADE_E2E_KEEP_CLUSTER" != "false" ]]; then
  echo "UPGRADE_E2E_KEEP_CLUSTER must be true or false." >&2
  exit 1
fi

for tool in "$CONTAINER_TOOL" "$GIT" "$HELM" "$KIND" "$KUBECTL" "$TAR" go; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "Required tool '$tool' is unavailable." >&2
    exit 1
  fi
done

kind_version_output="$("$KIND" version)"
read -r _ kind_version _ <<<"$kind_version_output"
if [[ "$kind_version" != "$KIND_UPGRADE_EXPECTED_VERSION" ]]; then
  echo "Kind version $kind_version does not match required $KIND_UPGRADE_EXPECTED_VERSION." >&2
  exit 1
fi

helm_version="$("$HELM" version --template '{{.Version}}')"
if [[ "$helm_version" != "$HELM_UPGRADE_EXPECTED_VERSION" ]]; then
  echo "Helm version $helm_version does not match required $HELM_UPGRADE_EXPECTED_VERSION." >&2
  exit 1
fi

echo "Using Kind $kind_version and Helm $helm_version with $KIND_EXPERIMENTAL_PROVIDER."

existing_clusters="$("$KIND" get clusters)"
while IFS= read -r existing_cluster; do
  if [[ "$existing_cluster" == "$KIND_CLUSTER" ]]; then
    echo "Dedicated Kind cluster '$KIND_CLUSTER' already exists; remove it before running the isolated suite." >&2
    exit 1
  fi
done <<<"$existing_clusters"

# Claim ownership immediately before creation so the EXIT trap also removes a
# partially created cluster when Kind fails.
cluster_owned=true
echo "Creating Kind cluster '$KIND_CLUSTER' with $KIND_EXPERIMENTAL_PROVIDER..."
"$KIND" create cluster \
  --name "$KIND_CLUSTER" \
  --image "$KIND_UPGRADE_NODE_IMAGE" \
  --kubeconfig "$KUBECONFIG"

go test -tags=e2e ./test/e2e/kind/upgrade -v -timeout=45m
