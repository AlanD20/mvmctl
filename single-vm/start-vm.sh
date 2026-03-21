#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

FIRECRACKER_SOCKET_PATH="${OUTPUT_DIR}/firecracker.socket"
FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"
FIRECRACKER_BIN="../assets/bin/firecracker"
KERNEL_PATH="../assets/kernels/vmlinux"
CONFIG_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.json"
CONSOLE_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.console.log"

echo "=== Starting/Resuming Firecracker VM ==="

# Check if already running by PID file
if [ -f "$FIRECRACKER_PID_FILE" ]; then
  EXISTING_PID=$(cat "$FIRECRACKER_PID_FILE")
  if kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "VM is already running (PID: $EXISTING_PID)"
    echo "View logs: tail -f ${OUTPUT_DIR}/firecracker.console.log"
    exit 0
  fi
fi

if [ ! -f "$FIRECRACKER_BIN" ] || [ ! -f "$KERNEL_PATH" ] || [ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]; then
  echo "Missing required files. Run ./setup.sh first."
  exit 1
fi

if ! ./network.sh check 2>/dev/null; then
  echo "Setting up network..."
  sudo ./network.sh
fi

echo "Starting Firecracker VM..."

if [ "$ENABLE_SOCKET" = "true" ]; then
  nohup "$FIRECRACKER_BIN" --api-sock "$FIRECRACKER_SOCKET_PATH" --config-file "$CONFIG_ABS_PATH" \
    >"$CONSOLE_ABS_PATH" 2>&1 &
else
  nohup "$FIRECRACKER_BIN" --no-api --config-file "$CONFIG_ABS_PATH" \
    >"$CONSOLE_ABS_PATH" 2>&1 &
fi

FIRECRACKER_PID=$!
echo "$FIRECRACKER_PID" >"$FIRECRACKER_PID_FILE"

sleep 2

# Verify process is running
if ! kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
  echo "ERROR: Firecracker failed to start. Check ${OUTPUT_DIR}/firecracker.console.log"
  exit 1
fi

echo ""
echo "=== VM Started ==="
echo "Firecracker PID: $FIRECRACKER_PID"
echo ""
echo "View console logs:"
echo "  tail -f ${OUTPUT_DIR}/firecracker.console.log"
echo ""
echo "To connect via SSH:"
echo "  ssh -i ${OUTPUT_DIR}/vm.id_rsa root@${GUEST_IP}"
echo ""
echo "Run ./stop-vm.sh to stop the VM"
echo "Run ./cleanup.sh to remove VM and clean up network"
