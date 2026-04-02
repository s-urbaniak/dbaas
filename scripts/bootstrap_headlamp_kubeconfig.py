#!/usr/bin/env python3
"""Bootstrap the headlamp-workspace-kubeconfig Secret with two static entries:

  root  — KCP root workspace (kcp-front-proxy in-cluster, admin credentials)
  kind  — kind cluster native API (kubernetes.default.svc, headlamp SA token)

Uses only Python stdlib + kubectl to avoid external dependencies.
"""

import base64
import json
import os
import subprocess
import sys
import tempfile


def kubectl(*args, input_data=None, kubeconfig=None):
    """Run kubectl and return stdout; raise on non-zero exit."""
    env = os.environ.copy()
    if kubeconfig:
        env["KUBECONFIG"] = kubeconfig
    result = subprocess.run(
        ["kubectl", *args],
        input=input_data,
        capture_output=True,
        text=True,
        env=env,
    )
    if result.returncode != 0:
        print(f"kubectl {' '.join(str(a) for a in args)} failed:\n{result.stderr}",
              file=sys.stderr)
        sys.exit(1)
    return result.stdout.strip()


def kconf(*args, kubeconfig):
    return kubectl("config", *args, kubeconfig=kubeconfig)


def write_b64_file(path, b64_data):
    """Decode base64 and write to path; return path or None if empty."""
    if not b64_data:
        return None
    with open(path, "wb") as f:
        f.write(base64.b64decode(b64_data))
    return path


with tempfile.TemporaryDirectory() as tmpdir:
    kcp_kc  = os.path.join(tmpdir, "kcp-admin.kubeconfig")
    head_kc = os.path.join(tmpdir, "headlamp.kubeconfig")
    ca_file  = os.path.join(tmpdir, "kcp-ca.crt")
    cert_file = os.path.join(tmpdir, "kcp-client.crt")
    key_file  = os.path.join(tmpdir, "kcp-client.key")
    kind_ca_file = os.path.join(tmpdir, "kind-ca.crt")

    # ── 1. Extract KCP admin kubeconfig ──────────────────────────────────────
    print("Loading kcp-admin-kubeconfig secret …")
    raw_kcp = kubectl(
        "get", "secret", "kcp-admin-kubeconfig",
        "-o", "jsonpath={.data.kubeconfig}",
    )
    with open(kcp_kc, "wb") as f:
        f.write(base64.b64decode(raw_kcp))

    kcp_server   = kconf("view", "--raw", "-o",
                         "jsonpath={.clusters[0].cluster.server}",
                         kubeconfig=kcp_kc)
    kcp_ca_b64   = kconf("view", "--raw", "-o",
                         "jsonpath={.clusters[0].cluster.certificate-authority-data}",
                         kubeconfig=kcp_kc)
    kcp_cert_b64 = kconf("view", "--raw", "-o",
                         "jsonpath={.users[0].user.client-certificate-data}",
                         kubeconfig=kcp_kc)
    kcp_key_b64  = kconf("view", "--raw", "-o",
                         "jsonpath={.users[0].user.client-key-data}",
                         kubeconfig=kcp_kc)
    kcp_insecure = kconf("view", "--raw", "-o",
                         "jsonpath={.clusters[0].cluster.insecure-skip-tls-verify}",
                         kubeconfig=kcp_kc)
    print(f"  KCP root server: {kcp_server}")

    # ── 2. Fetch kind cluster CA and generate headlamp token ─────────────────
    print("Fetching kind cluster CA …")
    kind_ca_pem = kubectl(
        "get", "cm", "kube-root-ca.crt",
        "-n", "kube-system",
        "-o", "jsonpath={.data.ca\\.crt}",
    )
    with open(kind_ca_file, "w") as f:
        f.write(kind_ca_pem)

    print("Generating headlamp SA token (10 years) …")
    kind_token = kubectl(
        "create", "token", "headlamp",
        "-n", "headlamp",
        "--duration=87600h",
    )

    # ── 3. Load existing headlamp-workspace-kubeconfig ───────────────────────
    print("Loading existing headlamp-workspace-kubeconfig …")
    raw_existing = kubectl(
        "get", "secret", "headlamp-workspace-kubeconfig",
        "-n", "headlamp",
        "-o", "jsonpath={.data.config}",
    )
    if raw_existing:
        with open(head_kc, "wb") as f:
            f.write(base64.b64decode(raw_existing))
    else:
        with open(head_kc, "w") as f:
            f.write("apiVersion: v1\nkind: Config\nclusters: []\n"
                    "contexts: []\nusers: []\ncurrent-context: ''\npreferences: {}\n")

    # ── 4. Upsert "root" context ──────────────────────────────────────────────
    kconf("set-cluster", "root", f"--server={kcp_server}", kubeconfig=head_kc)

    if write_b64_file(ca_file, kcp_ca_b64):
        kconf("set-cluster", "root",
              f"--certificate-authority={ca_file}",
              "--embed-certs=true",
              kubeconfig=head_kc)
    elif kcp_insecure == "true":
        kconf("set-cluster", "root",
              "--insecure-skip-tls-verify=true",
              kubeconfig=head_kc)

    if write_b64_file(cert_file, kcp_cert_b64) and write_b64_file(key_file, kcp_key_b64):
        kconf("set-credentials", "root",
              f"--client-certificate={cert_file}",
              f"--client-key={key_file}",
              "--embed-certs=true",
              kubeconfig=head_kc)

    kconf("set-context", "root", "--cluster=root", "--user=root", kubeconfig=head_kc)

    # ── 5. Upsert "kind" context ──────────────────────────────────────────────
    kconf("set-cluster", "kind",
          "--server=https://kubernetes.default.svc:443",
          f"--certificate-authority={kind_ca_file}",
          "--embed-certs=true",
          kubeconfig=head_kc)
    kconf("set-credentials", "kind",
          f"--token={kind_token}",
          kubeconfig=head_kc)
    kconf("set-context", "kind", "--cluster=kind", "--user=kind", kubeconfig=head_kc)

    # ── 6. Patch the Secret ───────────────────────────────────────────────────
    with open(head_kc, "rb") as f:
        config_b64 = base64.b64encode(f.read()).decode()

    patch = json.dumps({"data": {"config": config_b64}})
    kubectl(
        "patch", "secret", "headlamp-workspace-kubeconfig",
        "-n", "headlamp",
        "--type=merge",
        "-p", patch,
    )
    print("✓ headlamp-workspace-kubeconfig patched with 'root' and 'kind' contexts")
