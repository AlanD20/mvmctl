#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Cleaning up Firecracker Multi-VM ==="

# =============================================================================
# STEP 1: STOP ALL VMS
# =============================================================================
echo "[1/4] Stopping all VMs..."

if [ -d "${OUTPUT_DIR}" ]; then
  for vm_dir in "${OUTPUT_DIR}"/*/; do
    if [ -d "$vm_dir" ]; then
      VM_NAME=$(basename "$vm_dir")
      echo " - Stopping $VM_NAME..."

      # Stop by PID file
      if [ -f "$vm_dir/firecracker.pid" ]; then
        VM_PID=$(cat "$vm_dir/firecracker.pid")
        if kill -0 "$VM_PID" 2>/dev/null; then
          kill "$VM_PID" 2>/dev/null || true
          sleep 0.5
          kill -9 "$VM_PID" 2>/dev/null || true
        fi
        rm -f "$vm_dir/firecracker.pid"
      fi

      # Remove tap device
      TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
      if ip link show "$TAP_DEV" &>/dev/null; then
        ip link del "$TAP_DEV" 2>/dev/null || true
      fi
    fi
  done

  rm -rf "${OUTPUT_DIR}"
  echo " - All VMs stopped and removed"
else
  echo " - No VMs to stop"
fi

# =============================================================================
# STEP 2: REMOVE TAP DEVICES
# =============================================================================
echo "[2/4] Removing tap devices..."

for tap in $(ip link show type tap 2>/dev/null | grep -oE "${TAP_PREFIX}-[^[:space:]:]+" | sort -u); do
  echo " - Removing tap: $tap"
  ip link del "$tap" 2>/dev/null || true
done

# =============================================================================
# STEP 3: REMOVE BRIDGE
# =============================================================================
echo "[3/4] Removing bridge $BRIDGE_NAME..."

if ip link show "$BRIDGE_NAME" &>/dev/null; then
  ip link set "$BRIDGE_NAME" down 2>/dev/null || true
  ip link del "$BRIDGE_NAME" 2>/dev/null || true
  echo " - Bridge removed"
else
  echo " - Bridge does not exist"
fi

# =============================================================================
# STEP 4: FLUSH IPTABLES
# =============================================================================
echo "[4/4] Flushing iptables NAT rules..."

DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -n "$DEFAULT_IFACE" ]; then
  iptables -t nat -D POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
fi

echo " - NAT rules flushed"

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ Cleanup Complete ✓✓✓"
echo "=========================================="
echo ""
echo "Cleaned up:"
echo " - All VMs stopped and removed"
echo " - All tap devices removed"
echo " - Bridge $BRIDGE_NAME removed"
echo " - NAT rules flushed"
echo ""
echo "NOTE: IP forwarding is still enabled"
echo "To disable: sysctl -w net.ipv4.ip_forward=0"
echo ""
