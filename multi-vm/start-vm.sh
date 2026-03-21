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
  echo "Starts or resumes a specific VM"
  echo ""
  echo "Examples:"
  echo "  $0 vm1    # Start vm1"
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
  echo "ERROR: VM '$VM_NAME' does not exist. Create it first with:"
  echo "  ./create-vm.sh $VM_NAME [vcpu] [memory_mib]"
  exit 1
fi

# =============================================================================
# CHECK IF ALREADY RUNNING
# =============================================================================
PID_FILE="$VM_DIR/firecracker.pid"

if [ -f "$PID_FILE" ]; then
  EXISTING_PID=$(cat "$PID_FILE" 2>/dev/null)
  if [ -n "$EXISTING_PID" ] && kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "VM '$VM_NAME' is already running (PID: $EXISTING_PID)"
    echo ""
    echo "View console logs:"
    echo "  tail -f $VM_DIR/firecracker.console.log"
    exit 0
  fi
fi

# =============================================================================
# VALIDATE REQUIRED FILES
# =============================================================================
if [ ! -f "$VM_DIR/firecracker.json" ]; then
  echo "ERROR: Firecracker config not found at $VM_DIR/firecracker.json"
  echo "VM may be corrupted. Delete and recreate with:"
  echo "  ./delete-vm.sh $VM_NAME"
  echo "  ./create-vm.sh $VM_NAME"
  exit 1
fi

if [ ! -f "$VM_DIR/rootfs.ext4" ]; then
  echo "ERROR: Rootfs not found at $VM_DIR/rootfs.ext4"
  exit 1
fi

# =============================================================================
# VALIDATE NETWORK
# =============================================================================
echo "=== Starting VM: $VM_NAME ==="

# Get tap device name from config
TAP_DEV=$(grep -oP '"host_dev_name": "\K[^"]*' "$VM_DIR/firecracker.json" 2>/dev/null | head -1)
if [ -z "$TAP_DEV" ]; then
  TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
fi

# Check bridge exists
if ! ip link show "$BRIDGE_NAME" &>/dev/null; then
  echo "ERROR: Bridge $BRIDGE_NAME does not exist. Run ./setup.sh first"
  exit 1
fi

# Verify tap device exists (should have been created by create-vm.sh)
if ! ip link show "$TAP_DEV" &>/dev/null; then
  echo "ERROR: Tap device $TAP_DEV does not exist."
  echo "This VM must be created first with: ./create-vm.sh $VM_NAME"
  exit 1
fi

echo " - Network ready (tap: $TAP_DEV)"

# =============================================================================
# START VM
# =============================================================================
echo " - Starting Firecracker VM..."

cd "$VM_DIR"

FIRECRACKER_BIN="../../../assets/bin/firecracker"
SOCKET_FILE="${VM_NAME}.socket"
CONSOLE_LOG="firecracker.console.log"

# Start Firecracker
if [ "$ENABLE_SOCKET" = "true" ]; then
  nohup "$FIRECRACKER_BIN" --api-sock "$SOCKET_FILE" --config-file firecracker.json >"$CONSOLE_LOG" 2>&1 &
else
  nohup "$FIRECRACKER_BIN" --no-api --config-file firecracker.json >"$CONSOLE_LOG" 2>&1 &
fi

VM_PID=$!
echo "$VM_PID" >"$PID_FILE"

sleep 2

# Verify process started
if ! kill -0 "$VM_PID" 2>/dev/null; then
  echo "ERROR: Firecracker failed to start. Check $VM_DIR/firecracker.console.log"
  cd ../..
  exit 1
fi

cd ../..

# =============================================================================
# SUCCESS MESSAGE
# =============================================================================
echo ""
echo "=========================================="
echo "✓✓✓ VM Started Successfully ✓✓✓"
echo "=========================================="
echo ""
echo "VM Details:"
echo " - Name: $VM_NAME"
echo " - PID: $VM_PID"
echo " - Directory: $VM_DIR"
echo ""
echo "Commands:"
echo " - Console logs: tail -f $VM_DIR/firecracker.console.log"
echo " - Stop:        ./stop-vm.sh $VM_NAME"
echo " - Delete:      ./delete-vm.sh $VM_NAME"
echo " - Status:      ./list-vms.sh"
echo ""
