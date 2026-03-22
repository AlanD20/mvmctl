#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

VM_NAME="${1:-}"
LOG_TYPE="${2:-boot}"

if [ -z "$VM_NAME" ]; then
  echo "Usage: $0 <vm_name> [os|boot]"
  echo ""
  echo "Arguments:"
  echo "  vm_name - VM name (required)"
  echo "  type    - Log type: 'os' (firecracker.log) or 'boot' (firecracker.console.log)"
  echo "            Default: boot"
  echo ""
  echo "Examples:"
  echo "  $0 vm1       # Show boot log (console)"
  echo "  $0 vm1 os    # Show OS log (firecracker)"
  echo "  $0 vm1 boot  # Show boot log (console)"
  exit 1
fi

VM_DIR="${OUTPUT_DIR}/${VM_NAME}"

if [ ! -d "$VM_DIR" ]; then
  echo "ERROR: VM '$VM_NAME' not found at $VM_DIR"
  exit 1
fi

case "$LOG_TYPE" in
os)
  LOG_FILE="${VM_DIR}/firecracker.log"
  if [ ! -f "$LOG_FILE" ]; then
    echo "ERROR: OS log file not found: $LOG_FILE"
    exit 1
  fi
  echo "=== OS Log for $VM_NAME ==="
  echo "File: $LOG_FILE"
  echo "Press Ctrl+C to exit"
  echo ""
  tail -f "$LOG_FILE"
  ;;
boot)
  LOG_FILE="${VM_DIR}/firecracker.console.log"
  if [ ! -f "$LOG_FILE" ]; then
    echo "ERROR: Boot log file not found: $LOG_FILE"
    exit 1
  fi
  echo "=== Boot Log for $VM_NAME ==="
  echo "File: $LOG_FILE"
  echo "Press Ctrl+C to exit"
  echo ""
  tail -f "$LOG_FILE"
  ;;
*)
  echo "ERROR: Unknown log type '$LOG_TYPE'"
  echo "Valid types: os, boot"
  exit 1
  ;;
esac
