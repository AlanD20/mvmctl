#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

API_SOCKET="/tmp/firecracker.socket"
FIRECRACKER_PID_FILE="/tmp/firecracker.pid"

echo "=== Starting Firecracker VM ==="

if [ ! -f "firecracker" ] || [ ! -f "vmlinux" ] || [ ! -f "rootfs.ext4" ]; then
  echo "Missing required files. Run ./setup.sh first."
  exit 1
fi

if ! ./network.sh check 2>/dev/null; then
  echo "Setting up network..."
  ./network.sh
fi

rm -f "$API_SOCKET"

echo "Starting Firecracker..."
./firecracker --no-api --config-file firecracker.json &
FIRECRACKER_PID=$!
echo $FIRECRACKER_PID >"$FIRECRACKER_PID_FILE"

echo ""
echo "=== VM Started ==="
echo "Firecracker PID: $FIRECRACKER_PID"
echo "API Socket: $API_SOCKET"
echo ""
echo "Connect to serial console with: sudo screen -r $(whoami) or"
echo "Use: sudo microcom /dev/ttyS0"
echo ""
echo "Run ./cleanup.sh when done to stop VM and clean up network"
