#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

API_SOCKET="${OUTPUT_DIR}/firecracker.socket"
FIRECRACKER_PID_FILE="${OUTPUT_DIR}/firecracker.pid"

echo "=== Resuming Firecracker VM ==="

# Check if already running
if screen -list | grep -q "fc-single"; then
  echo "VM is already running in screen session 'fc-single'"
  echo "Connect with: sudo screen -r fc-single"
  exit 0
fi

# Check if PID file exists and process is still running
if [ -f "$FIRECRACKER_PID_FILE" ]; then
  EXISTING_PID=$(cat "$FIRECRACKER_PID_FILE")
  if kill -0 "$EXISTING_PID" 2>/dev/null; then
    echo "VM is already running (PID: $EXISTING_PID)"
    echo "Connect with: sudo screen -r fc-single"
    exit 0
  fi
fi

FIRECRACKER_BIN="../assets/bin/firecracker"
KERNEL_PATH="../assets/kernels/vmlinux"

if [ ! -f "$FIRECRACKER_BIN" ] || [ ! -f "$KERNEL_PATH" ] || [ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]; then
  echo "Missing required files. Run ./setup.sh first."
  exit 1
fi

if ! ./network.sh check 2>/dev/null; then
  echo "Setting up network..."
  sudo ./network.sh
fi

echo "Starting Firecracker in screen session 'fc-single'..."
if [ "$ENABLE_SOCKET" = "true" ]; then
  SOCKET_PATH="${OUTPUT_DIR}/firecracker.socket"
  screen -dmS fc-single "$FIRECRACKER_BIN" --api-sock "$SOCKET_PATH" --config-file "${OUTPUT_DIR}/firecracker.json"
  sleep 1
  FIRECRACKER_PID=$(pgrep -f "firecracker.*--api-sock.*${OUTPUT_DIR}") || FIRECRACKER_PID=$(pgrep -f "firecracker.*${OUTPUT_DIR}")
  if [ -z "$FIRECRACKER_PID" ]; then
    FIRECRACKER_PID=$(cat "$FIRECRACKER_PID_FILE" 2>/dev/null || pgrep -f "firecracker.*${OUTPUT_DIR}")
  fi
else
  screen -dmS fc-single "$FIRECRACKER_BIN" --no-api --config-file "${OUTPUT_DIR}/firecracker.json"
  sleep 1
  FIRECRACKER_PID=$(pgrep -f "firecracker --no-api --config-file ${OUTPUT_DIR}/firecracker.json")
fi
echo "$FIRECRACKER_PID" >"$FIRECRACKER_PID_FILE"

echo ""
echo "=== VM Resumed ==="
echo "Firecracker PID: $FIRECRACKER_PID"
echo ""
echo "Connect to serial console with: sudo screen -r fc-single"
echo "To detach from screen, press: Ctrl+A, then D"
echo ""
echo "Run ./stop-vm.sh to pause/stop the VM (preserves state)"
echo "Run ./cleanup.sh to fully remove VM and clean up network"
