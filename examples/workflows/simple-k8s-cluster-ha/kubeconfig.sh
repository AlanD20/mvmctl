#!/bin/bash
DIR="$(dirname "$0")"
KUBECONFIG="$DIR/.kubeconfig"
VIP="10.10.0.100"

mvm exec controller-1 -- cat /etc/kubernetes/admin.conf > "$KUBECONFIG" 2>/dev/null
if [ -f "$KUBECONFIG" ]; then
  sed -i "s|server: https://.*:6443|server: https://$VIP:6443|" "$KUBECONFIG" 2>/dev/null
  chmod 600 "$KUBECONFIG"
  echo "→ .kubeconfig written (VIP $VIP). Usage:"
  echo "  kubectl --kubeconfig=$KUBECONFIG get nodes"
  exit 0
fi

echo "No reachable controller" >&2
exit 1
