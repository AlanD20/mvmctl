#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

# Derive rootfs filename dynamically (e.g. rootfs.ext4, rootfs.btrfs)
ROOTFS_EXT="${ROOTFS_PATH##*.}"
ROOTFS_DEST="${OUTPUT_DIR}/rootfs.${ROOTFS_EXT}"

FIRECRACKER_BIN="../assets/bin/firecracker"
FIRECRACKER_SOCKET="${OUTPUT_DIR}/firecracker.socket"
FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"
CONFIG_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.json"
CONSOLE_LOG="${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.console.log"

echo "=== Starting Firecracker VM ==="
echo "Image source : ${IMAGE_SOURCE}"
echo ""

# -----------------------------------------------------------------------------
# Check if already running
# -----------------------------------------------------------------------------

if [ -f "$FIRECRACKER_PID_FILE" ]; then
  EXISTING_PID=$(cat "$FIRECRACKER_PID_FILE")
  if kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo " - VM is already running (PID: $EXISTING_PID)"
    echo " - Console log: ./logs-vm.sh boot"
    exit 0
  fi
fi

# -----------------------------------------------------------------------------
# Validate required files
# -----------------------------------------------------------------------------

if [ ! -f "$FIRECRACKER_BIN" ]; then
  echo "ERROR: Firecracker binary not found at $FIRECRACKER_BIN"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

if [ ! -f "$ROOTFS_DEST" ]; then
  echo "ERROR: Rootfs not found at $ROOTFS_DEST"
  echo "Run './setup.sh' first"
  exit 1
fi

if [ ! -f "$CONFIG_ABS_PATH" ]; then
  echo "ERROR: VM config not found at $CONFIG_ABS_PATH"
  echo "Run './setup.sh' first"
  exit 1
fi

# -----------------------------------------------------------------------------
# Start VM
# -----------------------------------------------------------------------------

echo " - Starting Firecracker..."

# Remove stale socket file if present (prevents permission errors on restart)
[ -S "$FIRECRACKER_SOCKET" ] && rm -f "$FIRECRACKER_SOCKET"

# Build argument list
FIRECRACKER_ARGS=""
[ "$ENABLE_PCI" = "true" ] && FIRECRACKER_ARGS="--enable-pci"

if [ "$ENABLE_SOCKET" = "true" ]; then
  FIRECRACKER_ARGS="$FIRECRACKER_ARGS --api-sock $FIRECRACKER_SOCKET"
else
  FIRECRACKER_ARGS="$FIRECRACKER_ARGS --no-api"
fi

# Trim leading whitespace
FIRECRACKER_ARGS="${FIRECRACKER_ARGS# }"

echo " - Args: $FIRECRACKER_ARGS"

nohup "$FIRECRACKER_BIN" $FIRECRACKER_ARGS --config-file "$CONFIG_ABS_PATH" \
  >"$CONSOLE_LOG" 2>&1 &

FIRECRACKER_PID=$!
echo "$FIRECRACKER_PID" >"$FIRECRACKER_PID_FILE"

# Give the process a moment to initialise, then verify it's still alive
sleep 2
if ! kill -0 "$FIRECRACKER_PID" 2>/dev/null; then
  echo "ERROR: Firecracker failed to start. Check the console log:"
  echo "  cat $CONSOLE_LOG"
  exit 1
fi

# -----------------------------------------------------------------------------

echo ""
echo "=== VM Started (PID: $FIRECRACKER_PID) ==="
echo ""
echo "Console : ./logs-vm.sh boot"
echo "SSH     : ssh -i ${OUTPUT_DIR}/vm.id_rsa root@${GUEST_IP}"
echo "Stop    : ./delete-vm.sh"
echo ""
