#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

LOG_TYPE="${1:-boot}"

case "$LOG_TYPE" in
os)
  LOG_FILE="${OUTPUT_DIR}/firecracker.log"
  if [ ! -f "$LOG_FILE" ]; then
    echo "ERROR: OS log file not found: $LOG_FILE"
    echo "Make sure the VM has been created."
    exit 1
  fi
  echo "=== OS Log ==="
  echo "File: $LOG_FILE"
  echo "Press Ctrl+C to exit"
  echo ""
  tail -f "$LOG_FILE"
  ;;
boot)
  LOG_FILE="${OUTPUT_DIR}/firecracker.console.log"
  if [ ! -f "$LOG_FILE" ]; then
    echo "ERROR: Boot log file not found: $LOG_FILE"
    echo "Make sure the VM has been started."
    exit 1
  fi
  echo "=== Boot Log (Console) ==="
  echo "File: $LOG_FILE"
  echo "Press Ctrl+C to exit"
  echo ""
  tail -f "$LOG_FILE"
  ;;
*)
  echo "Usage: $0 [os|boot]"
  echo ""
  echo "Arguments:"
  echo "  type - Log type: 'os' (firecracker.log) or 'boot' (firecracker.console.log)"
  echo "         Default: boot"
  echo ""
  echo "Examples:"
  echo "  $0       # Show boot log (console)"
  echo "  $0 os    # Show OS log (firecracker)"
  echo "  $0 boot  # Show boot log (console)"
  exit 1
  ;;
esac
