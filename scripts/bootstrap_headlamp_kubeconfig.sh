#!/usr/bin/env bash
set -euo pipefail

kubectl_cmd=${KUBECTL:-kubectl}

kubectl_wrap() {
  "$kubectl_cmd" "$@"
}

kconf() {
  KUBECONFIG="$1" "$kubectl_cmd" config "${@:2}"
}

write_b64_file() {
  local path=$1
  local b64_data=$2

  if [[ -z "${b64_data}" ]]; then
    return 1
  fi

  printf '%s' "${b64_data}" | base64 -d > "${path}"
}

tmpdir=$(mktemp -d)
trap 'rm -rf "${tmpdir}"' EXIT

kcp_kc="${tmpdir}/kcp-admin.kubeconfig"
head_kc="${tmpdir}/headlamp.kubeconfig"
ca_file="${tmpdir}/kcp-ca.crt"
cert_file="${tmpdir}/kcp-client.crt"
key_file="${tmpdir}/kcp-client.key"
kind_ca_file="${tmpdir}/kind-ca.crt"

echo "Loading kcp-admin-kubeconfig secret ..."
raw_kcp=$(kubectl_wrap get secret kcp-admin-kubeconfig -o jsonpath='{.data.kubeconfig}')
printf '%s' "${raw_kcp}" | base64 -d > "${kcp_kc}"

kcp_server=$(kconf "${kcp_kc}" view --raw -o jsonpath='{.clusters[0].cluster.server}')
kcp_ca_b64=$(kconf "${kcp_kc}" view --raw -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
kcp_cert_b64=$(kconf "${kcp_kc}" view --raw -o jsonpath='{.users[0].user.client-certificate-data}')
kcp_key_b64=$(kconf "${kcp_kc}" view --raw -o jsonpath='{.users[0].user.client-key-data}')
kcp_insecure=$(kconf "${kcp_kc}" view --raw -o jsonpath='{.clusters[0].cluster.insecure-skip-tls-verify}')
echo "  kcp root server: ${kcp_server}"

echo "Fetching kind cluster CA ..."
kubectl_wrap get cm kube-root-ca.crt -n kube-system -o jsonpath='{.data.ca\.crt}' > "${kind_ca_file}"

echo "Generating headlamp SA token (10 years) ..."
kind_token=$(kubectl_wrap create token headlamp -n headlamp --duration=87600h)

echo "Loading existing headlamp-workspace-kubeconfig ..."
if raw_existing=$(kubectl_wrap get secret headlamp-workspace-kubeconfig -n headlamp -o jsonpath='{.data.config}' 2>/dev/null); then
  if [[ -n "${raw_existing}" ]]; then
    printf '%s' "${raw_existing}" | base64 -d > "${head_kc}"
  else
    cat > "${head_kc}" <<'EOF'
apiVersion: v1
kind: Config
clusters: []
contexts: []
users: []
current-context: ""
preferences: {}
EOF
  fi
else
  cat > "${head_kc}" <<'EOF'
apiVersion: v1
kind: Config
clusters: []
contexts: []
users: []
current-context: ""
preferences: {}
EOF
fi

kconf "${head_kc}" set-cluster root --server="${kcp_server}"
if write_b64_file "${ca_file}" "${kcp_ca_b64}"; then
  kconf "${head_kc}" set-cluster root \
    --certificate-authority="${ca_file}" \
    --embed-certs=true
elif [[ "${kcp_insecure}" == "true" ]]; then
  kconf "${head_kc}" set-cluster root --insecure-skip-tls-verify=true
fi

if write_b64_file "${cert_file}" "${kcp_cert_b64}" && write_b64_file "${key_file}" "${kcp_key_b64}"; then
  kconf "${head_kc}" set-credentials root \
    --client-certificate="${cert_file}" \
    --client-key="${key_file}" \
    --embed-certs=true
fi
kconf "${head_kc}" set-context root --cluster=root --user=root

kconf "${head_kc}" set-cluster kind \
  --server=https://kubernetes.default.svc:443 \
  --certificate-authority="${kind_ca_file}" \
  --embed-certs=true
kconf "${head_kc}" set-credentials kind --token="${kind_token}"
kconf "${head_kc}" set-context kind --cluster=kind --user=kind

kubectl_wrap create secret generic headlamp-workspace-kubeconfig \
  -n headlamp \
  --from-file=config="${head_kc}" \
  --dry-run=client -o yaml | kubectl_wrap apply -f -

echo "✓ headlamp-workspace-kubeconfig updated with 'root' and 'kind' contexts"
