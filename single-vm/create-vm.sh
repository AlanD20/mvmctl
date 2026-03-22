#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

# =============================================================================
# CONFIGURATION
# =============================================================================
FIRECRACKER_SOCKET_PATH="${OUTPUT_DIR}/firecracker.socket"
FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"
FIRECRACKER_BIN="../assets/bin/firecracker"
CONFIG_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.json"
CONSOLE_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.console.log"

echo "=== Starting/Resuming Firecracker VM ==="
echo "Image Source: ${IMAGE_SOURCE:-firecracker-ci}"

# =============================================================================
# CHECK IF ALREADY RUNNING
# =============================================================================
if [ -f "$FIRECRACKER_PID_FILE" ]; then
  EXISTING_PID=$(cat "$FIRECRACKER_PID_FILE")
  if kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "VM is already running (PID: $EXISTING_PID)"
    echo "View logs: tail -f ${OUTPUT_DIR}/firecracker.console.log"
    exit 0
  fi
fi

# =============================================================================
# VALIDATE REQUIRED FILES
# =============================================================================
if [ ! -f "$FIRECRACKER_BIN" ]; then
  echo "ERROR: Firecracker binary not found at $FIRECRACKER_BIN"
  exit 1
fi

if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

if [ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]; then
  echo "ERROR: Rootfs not found at ${OUTPUT_DIR}/rootfs.ext4"
  echo "Run './setup.sh' first"
  exit 1
fi

# =============================================================================
# START VM
# =============================================================================
echo "Starting Firecracker VM..."

# Clean up any existing socket file (prevents permission errors)
if [ -S "$FIRECRACKER_SOCKET_PATH" ]; then
  echo " - Removing stale socket file..."
  rm -f "$FIRECRACKER_SOCKET_PATH"
fi

# Build firecracker command arguments
FIRECRACKER_ARGS=""

if [ "$ENABLE_PCI" = "true" ]; then
  FIRECRACKER_ARGS="--enable-pci"
fi

if [ "$ENABLE_SOCKET" = "true" ]; then
  FIRECRACKER_ARGS="$FIRECRACKER_ARGS --api-sock $FIRECRACKER_SOCKET_PATH"
else
  # Explicitly disable API when socket mode is off
  FIRECRACKER_ARGS="$FIRECRACKER_ARGS --no-api"
fi

# Remove leading space if present
FIRECRACKER_ARGS=$(echo "$FIRECRACKER_ARGS" | sed 's/^ *//')

# Start firecracker with the appropriate arguments
echo " - Firecracker args: $FIRECRACKER_ARGS"
nohup "$FIRECRACKER_BIN" $FIRECRACKER_ARGS --config-file "$CONFIG_ABS_PATH" \
  >"$CONSOLE_ABS_PATH" 2>&1 &

FIRECRACKER_PID=$!
echo "$FIRECRACKER_PID" >"$FIRECRACKER_PID_FILE"

sleep 2

# Verify process is running
if ! kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
  echo "ERROR: Firecracker failed to start. Check ${OUTPUT_DIR}/firecracker.console.log"
  exit 1
fi

# =============================================================================
# SUCCESS OUTPUT
# =============================================================================
echo ""
echo "=== VM Started ==="
echo "Firecracker PID: $FIRECRACKER_PID"
echo ""
echo "View console logs:"
echo " tail -f ${OUTPUT_DIR}/firecracker.console.log"
echo ""
echo "To connect via SSH:"
echo " ssh -i ${OUTPUT_DIR}/vm.id_rsa root@${GUEST_IP}"
echo ""
echo "Run ./delete-vm.sh to stop and remove the VM"
