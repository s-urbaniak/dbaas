#!/usr/bin/env bash
set -euo pipefail

kubectl_cmd=$1
source_kubeconfig=$2
tmp_kubeconfig=$3
server_url=$4
namespace=$5
secret_name=$6
secret_key=$7

cp "$source_kubeconfig" "$tmp_kubeconfig"
trap 'rm -f "$tmp_kubeconfig"' EXIT

cluster_name=$(
  KUBECONFIG="$tmp_kubeconfig" "$kubectl_cmd" config view -o jsonpath='{.clusters[0].name}'
)

KUBECONFIG="$tmp_kubeconfig" "$kubectl_cmd" config set-cluster \
  "$cluster_name" \
  --server="$server_url"

"$kubectl_cmd" -n "$namespace" create secret generic "$secret_name" \
  --from-file="${secret_key}=${tmp_kubeconfig}" \
  --dry-run=client -o yaml | "$kubectl_cmd" apply -f -
