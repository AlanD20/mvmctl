#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

# =============================================================================
# PARSE ARGUMENTS
# =============================================================================
VM_NAME="${1:-}"

if [ -z "$VM_NAME" ]; then
  echo "Usage: $0 <name>"
  echo ""
  echo "Deletes a VM permanently (stops if running, removes all files)"
  echo ""
  echo "Examples:"
  echo "  $0 vm1    # Delete vm1 completely"
  echo ""
  echo "Available VMs:"
  ./list-vms.sh 2>/dev/null || ls -1 ${OUTPUT_DIR}/*/ 2>/dev/null | grep -v base-rootfs || echo "  (none)"
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
if [ -f "$VM_DIR/firecracker.pid" ]; then
  VM_PID=$(cat "$VM_DIR/firecracker.pid" 2>/dev/null)
  if [ -n "$VM_PID" ] && kill -0 "$VM_PID" 2>/dev/null; then
    echo " - Stopping running VM..."
    ./stop-vm.sh "$VM_NAME" 2>/dev/null || true
  fi
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
