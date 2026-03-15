#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Full Cleanup: Firecracker Multi-VM ==="

if [ -d "vms" ]; then
  echo "[1/4] Stopping all VMs..."
  for vm_dir in vms/*/; do
    if [ -d "$vm_dir" ]; then
      VM_NAME=$(basename "$vm_dir")
      if [ -f "$vm_dir/firecracker.pid" ]; then
        VM_PID=$(cat "$vm_dir/firecracker.pid")
        if kill -0 "$VM_PID" 2>/dev/null; then
          kill "$VM_PID" 2>/dev/null || true
          sleep 0.5
          kill -9 "$VM_PID" 2>/dev/null || true
        fi
        rm -f "$vm_dir/firecracker.pid"
      fi
      TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
      if ip link show "$TAP_DEV" &>/dev/null; then
        ip link del "$TAP_DEV" 2>/dev/null || true
      fi
      echo "  Stopped: $VM_NAME"
    fi
  done
  rm -rf vms
  echo "  All VMs removed"
fi

echo "[2/4] Removing all tap devices..."
for tap in $(ip link show type tap | grep -oP "${TAP_PREFIX}-[^\s:]+" | sort -u); do
  ip link del "$tap" 2>/dev/null || true
  echo "  Removed: $tap"
done

echo "[3/4] Removing bridge $BRIDGE_NAME..."
if ip link show "$BRIDGE_NAME" &>/dev/null; then
  ip link set "$BRIDGE_NAME" down 2>/dev/null || true
  ip link del "$BRIDGE_NAME" 2>/dev/null || true
  echo "  Bridge $BRIDGE_NAME removed"
fi

echo "[4/4] Flushing iptables NAT rules..."
iptables -t nat -D POSTROUTING -o eth0 -j MASQUERADE 2>/dev/null || true
echo "  NAT rules flushed"

echo ""
echo "=== NOTE: IP forwarding is still enabled ==="
echo "# To disable: sysctl -w net.ipv4.ip_forward=0"
echo ""

echo "=== Cleanup Complete ==="
echo "  - All VMs stopped and removed"
echo "  - All tap devices removed"
echo "  - Bridge removed"
echo "  - NAT rules flushed"
