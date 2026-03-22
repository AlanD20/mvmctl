#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

# =============================================================================
# PARSE ARGUMENTS
# =============================================================================
VM_NAME="${1:-}"

if [ "$VM_NAME" = "" ]; then
  echo "Usage: $0 <name>"
  echo ""
  echo "Deletes a VM permanently (stops if running, removes all files)"
  echo ""
  echo "Examples:"
  echo "  $0 vm1    # Delete vm1 completely"
  echo ""
  echo "Available VMs:"
  ./list-vms.sh 2>/dev/null || ls -1 "$OUTPUT_DIR"/*/ 2>/dev/null | grep -v base-rootfs || echo "  (none)"
  exit 1
fi

VM_DIR="${OUTPUT_DIR}/${VM_NAME}"

# =============================================================================
# VALIDATE VM EXISTS
# =============================================================================
if [ ! -d "$VM_DIR" ]; then
  echo "ERROR: VM '$VM_NAME' does not exist"
  exit 1
fi

echo "=== Deleting VM: $VM_NAME ==="

# =============================================================================
# STOP VM IF RUNNING
# =============================================================================
PID_FILE="$VM_DIR/firecracker.pid"
SOCKET_FILE="$VM_DIR/${VM_NAME}.socket"

# Check if VM is running
if [ -f "$PID_FILE" ]; then
  VM_PID=$(cat "$PID_FILE" 2>/dev/null)

  if [ -n "$VM_PID" ] && kill -0 "$VM_PID" 2>/dev/null; then
    echo " - VM is running (PID: $VM_PID), stopping..."

    # Try graceful shutdown via API if socket mode
    if [ "$ENABLE_SOCKET" = "true" ] && [ -S "$SOCKET_FILE" ]; then
      echo " - Sending graceful shutdown (CtrlAltDel)..."
      if curl --unix-socket "$SOCKET_FILE" -s -X PUT \
        "http://localhost/actions" \
        -d '{ "action_type": "SendCtrlAltDel" }' 2>/dev/null; then
        echo " - Waiting for VM to shutdown (5s timeout)..."
        for i in {1..10}; do
          sleep 0.5
          if ! kill -0 "$VM_PID" 2>/dev/null; then
            echo " - VM shutdown gracefully"
            break
          fi
        done
        if kill -0 "$VM_PID" 2>/dev/null; then
          echo " - Graceful shutdown timeout, forcing stop..."
        fi
      fi
    fi

    # Force kill if still running
    if kill -0 "$VM_PID" 2>/dev/null; then
      echo " - Force stopping Firecracker (PID: $VM_PID)..."
      kill "$VM_PID" 2>/dev/null || true
      sleep 1

      if kill -0 "$VM_PID" 2>/dev/null; then
        echo " - Force killing with SIGKILL..."
        kill -9 "$VM_PID" 2>/dev/null || true
      fi
    fi
  fi
fi

# Clean up PID and socket files
rm -f "$PID_FILE" "$SOCKET_FILE" 2>/dev/null || true

TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
VM_IP=""
if [ -f "$VM_DIR/firecracker.json" ]; then
  VM_IP=$(grep -oP 'ip=\K[^:]*' "$VM_DIR/firecracker.json" 2>/dev/null | head -1 || true)
fi

if [ -n "$VM_IP" ]; then
  echo " - Removing SSH fingerprint for $VM_IP..."
  ssh-keygen -R "$VM_IP" 2>/dev/null || true
fi

DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -n "$DEFAULT_IFACE" ]; then
  echo " - Removing iptables rules for $TAP_DEV..."
  sudo iptables -t nat -D POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE 2>/dev/null || true
  sudo iptables -D FORWARD -i "$TAP_DEV" -o "$DEFAULT_IFACE" -j ACCEPT 2>/dev/null || true
  sudo iptables -D FORWARD -i "$DEFAULT_IFACE" -o "$TAP_DEV" -j ACCEPT 2>/dev/null || true
fi

# =============================================================================
# REMOVE TAP DEVICE (if still exists)
# =============================================================================
TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"

if ip link show "$TAP_DEV" &>/dev/null; then
  echo " - Removing tap device $TAP_DEV..."
  sudo ip link del "$TAP_DEV" 2>/dev/null || true
fi

# =============================================================================
# REMOVE VM DIRECTORY
# =============================================================================
echo " - Removing VM files..."
rm -rf "$VM_DIR"

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ VM Deleted ✓✓✓"
echo "=========================================="
echo ""
echo "VM '$VM_NAME' has been completely removed."
echo ""
echo "Remaining VMs:"
./list-vms.sh 2>/dev/null || echo "  (none)"
echo ""
