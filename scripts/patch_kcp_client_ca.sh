#!/usr/bin/env bash
set -euo pipefail

kubectl_cmd=$1
namespace=$2

echo "Waiting for kcp-client-ca and kcp-front-proxy-client-ca secrets..."
until "$kubectl_cmd" -n "$namespace" get secret kcp-client-ca kcp-front-proxy-client-ca >/dev/null 2>&1; do
  sleep 2
done

client_ca=$("$kubectl_cmd" -n "$namespace" get secret kcp-client-ca -o jsonpath='{.data.tls\.crt}' | base64 -d)
fp_ca=$("$kubectl_cmd" -n "$namespace" get secret kcp-front-proxy-client-ca -o jsonpath='{.data.tls\.crt}' | base64 -d)

"$kubectl_cmd" -n "$namespace" create secret generic kcp-combined-client-ca \
  --from-literal="tls.crt=${client_ca}"$'\n'"${fp_ca}" \
  --dry-run=client -o yaml | "$kubectl_cmd" apply -f -

idx=$(
  "$kubectl_cmd" -n "$namespace" get deployment kcp -o json | \
    python3 -c "import json, sys; vols=json.load(sys.stdin)['spec']['template']['spec']['volumes']; [print(i) for i, v in enumerate(vols) if v.get('name') == 'kcp-client-ca']"
)

"$kubectl_cmd" -n "$namespace" patch deployment kcp --type=json \
  -p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/volumes/${idx}/secret/secretName\",\"value\":\"kcp-combined-client-ca\"}]"

echo "✓ KCP deployment patched to trust kcp-front-proxy-client-ca for client auth"
