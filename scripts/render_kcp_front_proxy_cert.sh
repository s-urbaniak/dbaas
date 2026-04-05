#!/usr/bin/env bash
set -euo pipefail

namespace="${1:-kcp}"
extra_sans="${KCP_FRONT_PROXY_EXTRA_SANS:-}"

declare -A seen_dns=()
declare -A seen_ips=()
dns_names=("localhost" "kcp-front-proxy.kcp.svc.cluster.local")
ip_addresses=()

trim() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

is_ip_literal() {
  local value="$1"
  [[ "$value" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && return 0
  [[ "$value" =~ : ]] && return 0
  return 1
}

append_unique_dns() {
  local value="$1"
  if [[ -n "${seen_dns[$value]:-}" ]]; then
    return
  fi
  seen_dns["$value"]=1
  dns_names+=("$value")
}

append_unique_ip() {
  local value="$1"
  if [[ -n "${seen_ips[$value]:-}" ]]; then
    return
  fi
  seen_ips["$value"]=1
  ip_addresses+=("$value")
}

render_yaml_list() {
  local indent="$1"
  shift
  local value
  for value in "$@"; do
    printf '%s- "%s"\n' "$indent" "$value"
  done
}

render_manifest() {
  cat <<EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: kcp-front-proxy
  namespace: ${namespace}
spec:
  secretName: kcp-front-proxy-cert
  duration: 8760h
  renewBefore: 360h
  privateKey:
    algorithm: RSA
    size: 2048
  usages:
    - server auth
  dnsNames:
EOF

  render_yaml_list '    ' "${dns_names[@]}"

  if ((${#ip_addresses[@]} > 0)); then
    cat <<'EOF'
  ipAddresses:
EOF
    render_yaml_list '    ' "${ip_addresses[@]}"
  fi

  cat <<'EOF'
  issuerRef:
    name: kcp-server-issuer
    kind: Issuer
    group: cert-manager.io
EOF
}

for dns_name in "${dns_names[@]}"; do
  seen_dns["$dns_name"]=1
done

IFS=',' read -r -a san_entries <<< "$extra_sans"
for san in "${san_entries[@]}"; do
  san="$(trim "$san")"
  if [[ -z "$san" ]]; then
    continue
  fi
  if is_ip_literal "$san"; then
    append_unique_ip "$san"
    continue
  fi
  append_unique_dns "$san"
done

render_manifest
