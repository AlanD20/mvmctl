#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

VM_NAME="${1:-}"

if [ "$VM_NAME" = "" ]; then
  echo "Usage: $0 <name>"
  echo ""
  echo "Examples:"
  echo "  $0 vm1          # Stop and remove vm1"
  echo ""
  echo "Available VMs:"
  for dir in "${OUTPUT_DIR}"/*/; do
    if [ -d "$dir" ]; then
      echo " $(basename "$dir")"
    fi
  done
  exit 1
fi

VM_DIR="${OUTPUT_DIR}/$VM_NAME"

if [ ! -d "$VM_DIR" ]; then
  echo "ERROR: VM '$VM_NAME' does not exist"
  exit 1
fi

echo "=== Stopping VM: $VM_NAME ==="

if [ -f "$VM_DIR/firecracker.pid" ]; then
  VM_PID=$(cat "$VM_DIR/firecracker.pid")
  if kill -0 "$VM_PID" 2>/dev/null; then
    echo "Stopping Firecracker (PID: $VM_PID)..."
    kill "$VM_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$VM_PID" 2>/dev/null || true
  fi
  rm -f "$VM_DIR/firecracker.pid"
fi

TAP_DEV="${TAP_PREFIX}-${VM_NAME}-0"
if ip link show "$TAP_DEV" &>/dev/null; then
  echo "Removing tap device $TAP_DEV..."
  ip link del "$TAP_DEV" 2>/dev/null || true
fi

echo "Cleaning up VM directory..."
rm -rf "$VM_DIR"

echo ""
echo "=== VM Removed ==="
echo "  Name: $VM_NAME"
