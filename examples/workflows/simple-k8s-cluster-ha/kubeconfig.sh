#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
umask 077
file=$(mktemp .kubeconfig.XXXXXX)
trap 'rm -f "$file"' EXIT
for n in 1 2 3; do
  if mvm exec "controller-$n" -- cat /etc/kubernetes/admin.conf > "$file" && [[ -s $file ]]; then
    sed 's|server: https://.*:6443|server: https://10.10.0.100:6443|' "$file" > .kubeconfig
    chmod 600 .kubeconfig
    echo 'Wrote .kubeconfig for the floating API endpoint'
    exit 0
  fi
done
echo 'No reachable controller supplied a kubeconfig' >&2
exit 1
