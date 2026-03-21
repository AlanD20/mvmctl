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
  echo "Stops and removes a specific VM"
  echo ""
  echo "Examples:"
  echo "  $0 vm1    # Stop and remove vm1"
  echo ""
  echo "Available VMs:"
  for dir in "${OUTPUT_DIR}"/*/; do
    if [ -d "$dir" ]; then
      echo "  $(basename "$dir")"
    fi
  done
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

echo "=== Stopping VM: $VM_NAME ==="

# =============================================================================
# STOP FIRECRACKER PROCESS
# =============================================================================
stop_firecracker() {
  local VM_PID=""

  # Try to get PID from file
  if [ -f "$VM_DIR/firecracker.pid" ]; then
    VM_PID=$(cat "$VM_DIR/firecracker.pid")
  elif [ -f "$VM_DIR/${VM_NAME}.pid" ]; then
    VM_PID=$(cat "$VM_DIR/${VM_NAME}.pid")
  fi

  # Stop by PID
  if [ -n "$VM_PID" ] && kill -0 "$VM_PID" 2>/dev/null; then
    echo " - Stopping Firecracker (PID: $VM_PID)..."
    kill "$VM_PID" 2>/dev/null || true
    sleep 1

    # Force kill if still running
    if kill -0 "$VM_PID" 2>/dev/null; then
      echo " - Force killing..."
      kill -9 "$VM_PID" 2>/dev/null || true
    fi
  fi

  # Remove PID files
  rm -f "$VM_DIR/firecracker.pid" "$VM_DIR/${VM_NAME}.pid" 2>/dev/null || true
}

stop_firecracker

# =============================================================================
# REMOVE TAP DEVICE
# =============================================================================
TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"

if ip link show "$TAP_DEV" &>/dev/null; then
  echo " - Removing tap device $TAP_DEV..."
  sudo ip link del "$TAP_DEV" 2>/dev/null || true
fi

# =============================================================================
# CLEANUP VM DIRECTORY
# =============================================================================
echo " - Removing VM directory..."
rm -rf "$VM_DIR"

# =============================================================================
# COMPLETION MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ VM Removed ✓✓✓"
echo "=========================================="
echo ""
echo "VM '$VM_NAME' has been stopped and removed"
echo ""
echo "Remaining VMs:"
for dir in "${OUTPUT_DIR}"/*/; do
  if [ -d "$dir" ]; then
    echo "  $(basename "$dir")"
  fi
done
