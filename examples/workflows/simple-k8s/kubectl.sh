#!/bin/bash
DIR="$(dirname "$0")"
KUBECONFIG="$DIR/.kubeconfig"
VIP="10.10.0.100"

# Try VIP first (kube-vip manages this)
mvm exec controller-1 -- cat /etc/kubernetes/admin.conf > "$KUBECONFIG" 2>/dev/null
if [ -f "$KUBECONFIG" ]; then
  sed -i "s|server: https://.*:6443|server: https://$VIP:6443|" "$KUBECONFIG" 2>/dev/null
  chmod 600 "$KUBECONFIG"
  if kubectl --kubeconfig="$KUBECONFIG" "$@" 2>/dev/null; then
    exit 0
  fi
fi

# Fallback: try each controller directly
for ctrl in $(mvm vm ls 2>/dev/null | awk '/controller-/ {print $2}'); do
  mvm exec "$ctrl" -- cat /etc/kubernetes/admin.conf > "$KUBECONFIG" 2>/dev/null || continue
  IP=$(mvm vm inspect "$ctrl" --json 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin)['networking']['ipv4'])" 2>/dev/null) || continue
  sed -i "s|server: https://.*:6443|server: https://$IP:6443|" "$KUBECONFIG"
  chmod 600 "$KUBECONFIG"
  if kubectl --kubeconfig="$KUBECONFIG" "$@"; then
    exit 0
  fi
  echo "→ $ctrl unreachable, trying next..." >&2
done

echo "No reachable controller" >&2
exit 1
