#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

API_SOCKET="/tmp/firecracker.socket"
FIRECRACKER_PID_FILE="/tmp/firecracker.pid"

echo "=== Starting Firecracker VM ==="

FIRECRACKER_BIN="../assets/bin/firecracker"
KERNEL_PATH="../assets/kernels/vmlinux"

if [ ! -f "$FIRECRACKER_BIN" ] || [ ! -f "$KERNEL_PATH" ] || [ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]; then
  echo "Missing required files. Run ./setup.sh first."
  exit 1
fi

if ! ./network.sh check 2>/dev/null; then
  echo "Setting up network..."
  ./network.sh
fi

rm -f "$API_SOCKET"

echo "Starting Firecracker in screen session 'fc-single'..."
screen -dmS fc-single "$FIRECRACKER_BIN" --no-api --config-file "${OUTPUT_DIR}/firecracker.json"
# Wait a moment for process to start
sleep 1
FIRECRACKER_PID=$(pgrep -f "firecracker --no-api --config-file ${OUTPUT_DIR}/firecracker.json")
echo "$FIRECRACKER_PID" >"$FIRECRACKER_PID_FILE"

echo ""
echo "=== VM Started ==="
echo "Firecracker PID: $FIRECRACKER_PID"
echo ""
echo "Connect to serial console with: sudo screen -r fc-single"
echo "To detach from screen, press: Ctrl+A, then D"
echo ""
echo "Run ./cleanup.sh when done to stop VM and clean up network"
