#!/usr/bin/env bash
set -euo pipefail

kubectl_cmd=$1
namespace=$2
name=$3
key=$4
src=$5

tmp_manifest=$(mktemp)
trap 'rm -f "$tmp_manifest"' EXIT

"$kubectl_cmd" -n "$namespace" create configmap "$name" \
  --from-file="${key}=${src}" \
  --dry-run=client -o yaml > "$tmp_manifest"

"$kubectl_cmd" replace -f "$tmp_manifest" >/dev/null 2>&1 || \
  "$kubectl_cmd" create -f "$tmp_manifest"
